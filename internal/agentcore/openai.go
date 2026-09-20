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
	"time"
)

const (
	openAIModelDiscoveryTimeout       = 5 * time.Second
	openAIModelDiscoveryRetryCooldown = 15 * time.Second
	openAIModelDiscoveryMaxBody       = int64(1 << 20)
)

var openAIReservedProviderOptions = map[string]struct{}{
	"model": {}, "messages": {}, "stream": {}, "stream_options": {}, "tools": {},
	"max_tokens": {}, "temperature": {}, "top_p": {}, "top_k": {},
	"frequency_penalty": {}, "presence_penalty": {}, "reasoning_effort": {},
}

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

	mu                       sync.RWMutex
	models                   []Model
	discoveryDone            bool
	discoveryInFlight        bool
	discoveryClosed          bool
	discoveryNextAttempt     time.Time
	discoveryCancel          context.CancelFunc
	discoveryWG              sync.WaitGroup
	discoveryTimeout         time.Duration
	discoveryRetryCooldown   time.Duration
	discoveryResponseMaxBody int64
}

func newOpenAI(name, baseURL, apiKey string, discover bool, extraHeaders []string, static []Model) *openaiProvider {
	return newOpenAIWithType(name, "openai", baseURL, apiKey, discover, extraHeaders, static)
}

func newOpenAIWithType(name, wireType, baseURL, apiKey string, discover bool, extraHeaders []string, static []Model) *openaiProvider {
	return &openaiProvider{
		name:                     name,
		wireType:                 wireType,
		baseURL:                  strings.TrimRight(baseURL, "/"),
		apiKey:                   apiKey,
		discoverModels:           discover,
		extraHeaders:             extraHeaders,
		httpc:                    &http.Client{Transport: providerTransport()},
		models:                   append([]Model(nil), static...),
		discoveryTimeout:         openAIModelDiscoveryTimeout,
		discoveryRetryCooldown:   openAIModelDiscoveryRetryCooldown,
		discoveryResponseMaxBody: openAIModelDiscoveryMaxBody,
	}
}

func (p *openaiProvider) Name() string { return p.name }

// Type returns the wire protocol name.
func (p *openaiProvider) Type() string { return p.wireType }

// Discoverable reports whether the provider fetches its model list live
// (local providers); unknown IDs are then allowed preflight.
func (p *openaiProvider) Discoverable() bool { return p.discoverModels }

// Models returns an immutable snapshot immediately. When discovery is enabled,
// the first eligible call starts a single background fetch; a later call sees
// the atomically published result. Discovery failures leave the current
// snapshot untouched and become eligible for retry after a cooldown.
func (p *openaiProvider) Models() []Model {
	p.mu.Lock()
	models := append([]Model(nil), p.models...)
	if p.discoveryEligibleLocked(time.Now()) {
		p.discoveryInFlight = true
		ctx, cancel := context.WithTimeout(context.Background(), p.discoveryTimeout)
		p.discoveryCancel = cancel
		p.discoveryWG.Add(1)
		safeGo("openai model discovery", func() {
			p.runModelDiscovery(ctx, cancel)
		})
	}
	p.mu.Unlock()
	return models
}

func (p *openaiProvider) discoveryEligibleLocked(now time.Time) bool {
	return p.discoverModels &&
		!p.discoveryDone &&
		!p.discoveryInFlight &&
		!p.discoveryClosed &&
		!now.Before(p.discoveryNextAttempt)
}

func (p *openaiProvider) runModelDiscovery(ctx context.Context, cancel context.CancelFunc) {
	defer p.discoveryWG.Done()
	defer cancel()

	discovered, err := p.fetchModels(ctx)
	now := time.Now()

	p.mu.Lock()
	defer p.mu.Unlock()
	p.discoveryInFlight = false
	p.discoveryCancel = nil
	if p.discoveryClosed {
		return
	}
	if err != nil {
		p.discoveryNextAttempt = now.Add(p.discoveryRetryCooldown)
		return
	}
	p.models = mergeModels(p.models, discovered)
	p.discoveryDone = true
	p.discoveryNextAttempt = time.Time{}
}

// Close cancels an in-flight discovery and waits for its goroutine to release
// the response body. It is idempotent. Provider streams have caller-owned
// contexts and are intentionally unaffected.
func (p *openaiProvider) Close() error {
	p.mu.Lock()
	p.discoveryClosed = true
	cancel := p.discoveryCancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.discoveryWG.Wait()
	p.httpc.CloseIdleConnections()
	return nil
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

func (p *openaiProvider) fetchModels(ctx context.Context) ([]Model, error) {
	u := p.baseURL + "/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	for _, h := range p.extraHeaders {
		if k, v, ok := strings.Cut(h, " "); ok {
			req.Header.Set(k, v)
		}
	}
	resp, err := p.httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, p.discoveryResponseMaxBody+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > p.discoveryResponseMaxBody {
		return nil, fmt.Errorf("GET %s: model response exceeds %d bytes", u, p.discoveryResponseMaxBody)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
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
	if err := validateUsage(usage); err != nil {
		return Cost{}, err
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, m := range p.models {
		if m.ID == req.Model {
			cost := CostOf(m, usage)
			if err := validateCost(cost); err != nil {
				return Cost{}, err
			}
			return cost, nil
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
	msgs := make([]chatMsg, 0, len(req.System)+len(req.Messages))
	// Keep the stable system prefix in caller order. Loop-added dynamic system
	// reminders live in req.Messages and therefore follow these static blocks.
	for _, s := range req.System {
		msgs = append(msgs, chatMsg{Role: "system", Content: s.Content})
	}
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
			if _, reserved := openAIReservedProviderOptions[k]; reserved {
				return nil, fmt.Errorf("openai: provider option %q is reserved; use normalized request fields", k)
			}
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
		sendStreamEvent(ctx, ch, NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: err.Error()}))
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
		if !errors.Is(err, context.Canceled) {
			sendStreamEvent(ctx, ch, NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: err.Error()}))
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		sendStreamEvent(ctx, ch, NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: parseAPIError(resp.Status, body)}))
		return
	}
	// Idle watchdog: a provider that goes silent mid-stream must fail the
	// turn instead of hanging the run forever.
	resp.Body = newTimeoutReader(resp.Body, streamIdleTimeout)

	var seq uint64
	type toolCallAccumulator struct {
		id   strings.Builder
		name strings.Builder
		args strings.Builder
	}
	toolAcc := map[int]*toolCallAccumulator{}
	toolOrder := []int{}
	toolPayloadBytes := 0
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
			sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: "invalid OpenAI stream event: " + err.Error()}))
			return
		}
		if chunk.Usage != nil {
			// OpenAI's prompt_tokens is inclusive of cached tokens, while the
			// normalized Usage contract keeps uncached, cache-create and cache-hit
			// buckets disjoint (matching Anthropic and CostOf). Subtract the cache
			// detail here so context meters and pricing never count it twice.
			cacheCreate := chunk.Usage.Details.CacheCreate
			cacheHit := chunk.Usage.Details.CacheHit
			if chunk.Usage.InputTokens < 0 || chunk.Usage.OutputTokens < 0 || cacheCreate < 0 || cacheHit < 0 ||
				cacheCreate > chunk.Usage.InputTokens || cacheHit > chunk.Usage.InputTokens-cacheCreate {
				sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: "OpenAI returned invalid token usage"}))
				return
			}
			uncachedInput := max(chunk.Usage.InputTokens-cacheCreate-cacheHit, 0)
			usage = &Usage{
				InputTokens:       uncachedInput,
				OutputTokens:      chunk.Usage.OutputTokens,
				CacheCreateTokens: cacheCreate,
				CacheHitTokens:    cacheHit,
			}
		}
		for _, c := range chunk.Choices {
			if c.FinishReason != "" {
				completed = true
			}
			if c.Delta.Content != "" {
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvTextDelta, TextDelta{Text: c.Delta.Content})) {
					return
				}
			}
			if r := c.Delta.ReasoningContent; r != "" {
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, ReasoningDelta{Text: r})) {
					return
				}
			} else if r := c.Delta.Reasoning; r != "" {
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, ReasoningDelta{Text: r})) {
					return
				}
			}
			for _, tc := range c.Delta.ToolCalls {
				acc, ok := toolAcc[tc.Index]
				if !ok {
					if len(toolAcc) >= providerToolCallLimit {
						sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: fmt.Sprintf("OpenAI stream exceeded %d tool calls", providerToolCallLimit)}))
						return
					}
					acc = &toolCallAccumulator{}
					toolAcc[tc.Index] = acc
					toolOrder = append(toolOrder, tc.Index)
				}
				additional := len(tc.ID) + len(tc.Function.Name) + len(tc.Function.Arguments)
				if additional > 0 && additional > providerToolPayloadLimit-toolPayloadBytes {
					sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: fmt.Sprintf("OpenAI tool calls exceeded %d-byte payload limit", providerToolPayloadLimit)}))
					return
				}
				toolPayloadBytes += additional
				acc.id.WriteString(tc.ID)
				acc.name.WriteString(tc.Function.Name)
				acc.args.WriteString(tc.Function.Arguments)
			}
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: err.Error()}))
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	if !completed {
		sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: "OpenAI stream ended before a completion marker"}))
		return
	}
	for _, idx := range toolOrder {
		tc := toolAcc[idx]
		if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvToolCall, ToolCall{ID: tc.id.String(), Name: tc.name.String(), Args: tc.args.String()})) {
			return
		}
	}
	if usage == nil {
		usage = &Usage{}
	}
	cost, err := p.Cost(req, *usage)
	if err != nil {
		sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: err.Error()}))
		return
	}
	sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvDone, Done{Usage: usage, Cost: &cost}))
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
