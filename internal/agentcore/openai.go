package agentcore

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// openaiProvider implements Provider for the OpenAI Chat Completions wire
// protocol over SSE — used for openai, openai-compat, ollama, llamacpp,
// lmstudio, and litellm.
type openaiProvider struct {
	name           string
	wireType       string
	baseURL        string
	apiKey         string
	discoverModels bool
	extraHeaders   []string // "K V" pairs
	httpc          *http.Client

	mu      sync.RWMutex
	models  []Model
	fetched bool
}

func newOpenAI(name, baseURL, apiKey string, discover bool, extraHeaders []string, static []Model) *openaiProvider {
	return newOpenAIWithType(name, "openai", baseURL, apiKey, discover, extraHeaders, static)
}

func newOpenAIWithType(name, wireType, baseURL, apiKey string, discover bool, extraHeaders []string, static []Model) *openaiProvider {
	return &openaiProvider{
		name:           name,
		wireType:       wireType,
		baseURL:        strings.TrimRight(baseURL, "/"),
		apiKey:         apiKey,
		discoverModels: discover,
		extraHeaders:   extraHeaders,
		httpc:          &http.Client{Transport: providerTransport()},
		models:         append([]Model(nil), static...),
	}
}

func (p *openaiProvider) Name() string { return p.name }

// Type returns the wire protocol name.
func (p *openaiProvider) Type() string { return p.wireType }

// Discoverable reports whether the provider fetches its model list live
// (local providers); unknown IDs are then allowed preflight.
func (p *openaiProvider) Discoverable() bool { return p.discoverModels }

// Models returns the static models, plus discovered ones on first call when
// discovery is enabled. Discovery failures are silent: the static list is
// returned unchanged.
func (p *openaiProvider) Models() []Model {
	p.mu.RLock()
	done := p.fetched
	p.mu.RUnlock()
	if p.discoverModels && !done {
		p.mu.Lock()
		if !p.fetched {
			p.fetched = true
			if discovered, err := p.fetchModels(); err == nil {
				p.models = mergeModels(p.models, discovered)
			}
		}
		p.mu.Unlock()
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Model(nil), p.models...)
}

func mergeModels(static, discovered []Model) []Model {
	seen := map[string]bool{}
	var out []Model
	for _, m := range append(append([]Model(nil), static...), discovered...) {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	return out
}

func (p *openaiProvider) fetchModels() ([]Model, error) {
	u := p.baseURL + "/models"
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	models := make([]Model, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, Model{ID: m.ID})
	}
	return models, nil
}

// CostOf uses the provider's model pricing, zero when the model is unknown.
func (p *openaiProvider) Cost(req Request, usage Usage) (Cost, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, m := range p.models {
		if m.ID == req.Model {
			return CostOf(m, usage), nil
		}
	}
	return Cost{}, nil
}

func (p *openaiProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	if p.apiKey == "" && p.name != "ollama" && p.name != "llamacpp" && p.name != "lmstudio" && p.name != "litellm" {
		return nil, fmt.Errorf("provider %s: no API key configured", p.name)
	}
	payload, err := p.buildBody(req)
	if err != nil {
		return nil, err
	}
	ch := make(chan StreamEvent, 64)
	safeGo("openai stream", func() { p.stream(ctx, req, payload, ch) })
	return ch, nil
}

func (p *openaiProvider) buildBody(req Request) ([]byte, error) {
	type chatMsg struct {
		Role       string     `json:"role"`
		Content    any        `json:"content,omitempty"`
		Reasoning  string     `json:"reasoning_content,omitempty"`
		ToolCalls  []toolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
	}
	type toolDef struct {
		Type     string         `json:"type"`
		Function map[string]any `json:"function"`
	}
	body := map[string]any{
		"model":          req.Model,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	var msgs []chatMsg
	for _, m := range req.Messages {
		cm := chatMsg{Role: m.Role, ToolCallID: m.ToolCallID, Reasoning: m.Reasoning}
		if m.Content != "" {
			cm.Content = m.Content
		}
		for _, tc := range m.ToolCalls {
			cm.ToolCalls = append(cm.ToolCalls, toolCall{ID: tc.ID, Type: "function", Function: toolFunction{Name: tc.Name, Arguments: tc.Args}})
		}
		msgs = append(msgs, cm)
	}
	for _, s := range req.System {
		msgs = append([]chatMsg{{Role: "system", Content: s.Content}}, msgs...)
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		var tools []toolDef
		for _, t := range req.Tools {
			fn := map[string]any{"name": t.Name, "description": t.Description}
			if t.InputSchema != nil {
				fn["parameters"] = t.InputSchema
			}
			tools = append(tools, toolDef{Type: "function", Function: fn})
		}
		body["tools"] = tools
	}
	s := req.Sampling
	if !ValidReasoningEffort(s.ReasoningEffort) {
		return nil, fmt.Errorf("reasoning effort %q is invalid", s.ReasoningEffort)
	}
	reasoningEffort := NormalizeReasoningEffort(s.ReasoningEffort)
	if s.MaxTokens > 0 {
		body["max_tokens"] = s.MaxTokens
	}
	if s.Temperature != nil {
		body["temperature"] = *s.Temperature
	}
	if s.TopP != nil {
		body["top_p"] = *s.TopP
	}
	if s.TopK > 0 {
		body["top_k"] = s.TopK
	}
	if s.FrequencyPenalty != nil {
		body["frequency_penalty"] = *s.FrequencyPenalty
	}
	if s.PresencePenalty != nil {
		body["presence_penalty"] = *s.PresencePenalty
	}
	if len(s.ProviderOptions) > 0 {
		for k, v := range s.ProviderOptions {
			body[k] = v
		}
	}
	if reasoningEffort != "" {
		body["reasoning_effort"] = reasoningEffort
	}
	return json.Marshal(body)
}

func (p *openaiProvider) stream(ctx context.Context, req Request, payload []byte, ch chan<- StreamEvent) {
	defer close(ch)
	u := p.baseURL + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		ch <- NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for _, h := range p.extraHeaders {
		if k, v, ok := strings.Cut(h, " "); ok {
			httpReq.Header.Set(k, v)
		}
	}
	resp, err := doRequestWithRetry(ctx, p.httpc, httpReq)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			ch <- NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: "cancelled"})
			return
		}
		ch <- NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: err.Error()})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		ch <- NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: parseAPIError(resp.Status, body)})
		return
	}
	// Idle watchdog: a provider that goes silent mid-stream must fail the
	// turn instead of hanging the run forever.
	resp.Body = newTimeoutReader(resp.Body, streamIdleTimeout)

	var seq uint64
	toolAcc := map[int]*toolCall{}
	toolOrder := []int{}
	var usage *Usage
	completed := false
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			completed = true
			break
		}
		var chunk struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					Reasoning        string `json:"reasoning"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				InputTokens  int `json:"prompt_tokens"`
				OutputTokens int `json:"completion_tokens"`
				Details      struct {
					CacheCreate int `json:"cache_creation"`
					CacheHit    int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			ch <- NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: "invalid OpenAI stream event: " + err.Error()})
			return
		}
		if chunk.Usage != nil {
			usage = &Usage{
				InputTokens:       chunk.Usage.InputTokens,
				OutputTokens:      chunk.Usage.OutputTokens,
				CacheCreateTokens: chunk.Usage.Details.CacheCreate,
				CacheHitTokens:    chunk.Usage.Details.CacheHit,
			}
		}
		for _, c := range chunk.Choices {
			if c.FinishReason != "" {
				completed = true
			}
			if c.Delta.Content != "" {
				ch <- NewEvent(&seq, RoleOrchestrator, EvTextDelta, TextDelta{Text: c.Delta.Content})
			}
			if r := c.Delta.ReasoningContent; r != "" {
				ch <- NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, ReasoningDelta{Text: r})
			} else if r := c.Delta.Reasoning; r != "" {
				ch <- NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, ReasoningDelta{Text: r})
			}
			for _, tc := range c.Delta.ToolCalls {
				acc, ok := toolAcc[tc.Index]
				if !ok {
					acc = &toolCall{Index: tc.Index, Type: "function"}
					toolAcc[tc.Index] = acc
					toolOrder = append(toolOrder, tc.Index)
				}
				if tc.ID != "" {
					acc.ID = tc.ID
				}
				if tc.Function.Name != "" {
					acc.Function.Name = tc.Function.Name
				}
				acc.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		ch <- NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: err.Error()})
		return
	}
	if err := ctx.Err(); err != nil {
		ch <- NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: err.Error()})
		return
	}
	if !completed {
		ch <- NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: "OpenAI stream ended before a completion marker"})
		return
	}
	for _, idx := range toolOrder {
		tc := toolAcc[idx]
		ch <- NewEvent(&seq, RoleOrchestrator, EvToolCall, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
	}
	if usage == nil {
		usage = &Usage{}
	}
	cost, _ := p.Cost(req, *usage)
	ch <- NewEvent(&seq, RoleOrchestrator, EvDone, Done{Usage: usage, Cost: &cost})
}

// toolCall mirrors the wire shape for assistant tool_calls.
type toolCall struct {
	ID       string       `json:"id"`
	Index    int          `json:"index"`
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

type toolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}
