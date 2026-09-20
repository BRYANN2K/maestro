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
	"os"
	"sort"
	"strings"
	"sync"
)

const anthropicVersion = "2023-06-01"

var (
	cacheOnce sync.Once
	cacheOn   = true
)

var anthropicReservedProviderOptions = map[string]struct{}{
	"model": {}, "system": {}, "messages": {}, "stream": {}, "tools": {},
	"max_tokens": {}, "temperature": {}, "top_p": {}, "top_k": {},
	"thinking": {}, "output_config": {},
}

// promptCacheEnabled reports whether Anthropic prompt-caching directives are
// sent (last system block, last 2 messages, last tool definition). Some Anthropic-
// compatible proxies reject cache_control blocks; disable with
// MAESTRO_NO_CACHE=1.
func promptCacheEnabled() bool {
	cacheOnce.Do(func() {
		cacheOn = os.Getenv("MAESTRO_NO_CACHE") == ""
	})
	return cacheOn
}

// cacheControl marks a content block for ephemeral prompt caching.
func cacheControl() map[string]any {
	return map[string]any{"type": "ephemeral"}
}

// anthropicProvider implements Provider for the Anthropic Messages API over
// SSE.
type anthropicProvider struct {
	name    string
	baseURL string
	apiKey  string
	httpc   *http.Client

	mu     sync.RWMutex
	models []Model
}

func newAnthropic(name, baseURL, apiKey string, static []Model) *anthropicProvider {
	if baseURL == "" {
		baseURL = "https://api.anthropic.com"
	}
	return &anthropicProvider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		httpc:   &http.Client{Transport: providerTransport()},
		models:  append([]Model(nil), static...),
	}
}

func (p *anthropicProvider) Name() string { return p.name }

// Type returns the wire protocol name.
func (p *anthropicProvider) Type() string { return "anthropic" }

// Close releases pooled idle connections. Active streams remain governed by
// their caller contexts, so orchestrator shutdown stays deterministic.
func (p *anthropicProvider) Close() error {
	p.httpc.CloseIdleConnections()
	return nil
}

func (p *anthropicProvider) Models() []Model {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return append([]Model(nil), p.models...)
}

func (p *anthropicProvider) Cost(req Request, usage Usage) (Cost, error) {
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

func (p *anthropicProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("provider %s: no API key configured", p.name)
	}
	payload, err := p.buildBody(req)
	if err != nil {
		return nil, err
	}
	ch := make(chan StreamEvent, 64)
	safeGo("anthropic stream", func() { p.stream(ctx, req, payload, ch) })
	return ch, nil
}

func (p *anthropicProvider) buildBody(req Request) ([]byte, error) {
	type toolDef struct {
		Name         string         `json:"name"`
		Description  string         `json:"description"`
		InputSchema  map[string]any `json:"input_schema"`
		CacheControl map[string]any `json:"cache_control,omitempty"`
	}
	cache := promptCacheEnabled()
	body := map[string]any{"model": req.Model, "stream": true}
	maxTokens := req.Sampling.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	body["max_tokens"] = maxTokens

	systemMessages := append([]Message(nil), req.System...)
	for _, message := range req.Messages {
		if message.Role == "system" && message.Content != "" {
			systemMessages = append(systemMessages, message)
		}
	}
	if len(systemMessages) > 0 {
		sys := make([]any, 0, len(systemMessages))
		for _, m := range systemMessages {
			if m.Content != "" {
				sys = append(sys, map[string]any{"type": "text", "text": m.Content})
			}
		}
		if len(sys) > 0 {
			// A single breakpoint covers the complete static + dynamic system
			// prefix. Dynamic guardrail messages are appended after req.System,
			// so the marker remains on the actual final system block.
			if cache {
				sys[len(sys)-1].(map[string]any)["cache_control"] = cacheControl()
			}
			body["system"] = sys
		}
	}
	// Reserve at most two rolling breakpoints for conversation history. Together
	// with the final system and tool breakpoints below, this stays within
	// Anthropic's four-explicit-breakpoint request limit.
	messageCount := 0
	for _, message := range req.Messages {
		switch message.Role {
		case "user", "assistant", "tool":
			messageCount++
		}
	}
	lastCacheable := messageCount - 2
	if lastCacheable < 0 {
		lastCacheable = 0
	}
	var msgs []map[string]any
	messageIndex := 0
	for _, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" && m.Role != "tool" && m.Role != "system" {
			continue
		}
		cached := cache && messageIndex >= lastCacheable
		switch m.Role {
		case "system":
			// Anthropic accepts system instructions only through the top-level
			// field. Dynamic guardrail/anti-loop reminders from Loop.History were
			// previously dropped here; they are promoted above instead.
			continue
		case "user":
			block := map[string]any{"type": "text", "text": m.Content}
			if cached {
				block["cache_control"] = cacheControl()
			}
			msgs = append(msgs, map[string]any{"role": "user", "content": []any{block}})
		case "assistant":
			content, err := anthropicAssistantContent(m, cached)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, map[string]any{"role": "assistant", "content": content})
		case "tool":
			block := map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}
			if cached {
				block["cache_control"] = cacheControl()
			}
			msgs = append(msgs, map[string]any{"role": "user", "content": []any{block}})
		}
		messageIndex++
	}
	body["messages"] = msgs

	if len(req.Tools) > 0 {
		tools := make([]toolDef, 0, len(req.Tools))
		for i, t := range req.Tools {
			td := toolDef{Name: t.Name, Description: t.Description}
			if t.InputSchema != nil {
				td.InputSchema = t.InputSchema
			} else {
				td.InputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if cache && i == len(req.Tools)-1 {
				td.CacheControl = cacheControl()
			}
			tools = append(tools, td)
		}
		body["tools"] = tools
	}
	s := req.Sampling
	if s.Temperature != nil {
		body["temperature"] = *s.Temperature
	}
	if s.TopP != nil {
		body["top_p"] = *s.TopP
	}
	if s.TopK > 0 {
		body["top_k"] = s.TopK
	}
	if len(s.ProviderOptions) > 0 {
		for k, v := range s.ProviderOptions {
			if _, reserved := anthropicReservedProviderOptions[k]; reserved {
				return nil, fmt.Errorf("anthropic: provider option %q is reserved; use normalized request fields", k)
			}
			body[k] = v
		}
	}
	if err := applyAnthropicReasoning(body, req.Model, s, maxTokens); err != nil {
		return nil, err
	}
	return json.Marshal(body)
}

type anthropicContentItem struct {
	index   int
	indexed bool
	seq     int
	block   map[string]any
}

// anthropicAssistantContent replays signed thinking blocks alongside their
// tool_use blocks. Indexed blocks retain the exact provider order; hand-built
// messages keep the historical thinking → text → tools order.
func anthropicAssistantContent(m Message, cached bool) ([]any, error) {
	items := make([]anthropicContentItem, 0, len(m.ThinkingBlocks)+len(m.ToolCalls)+1)
	seq := 0
	for _, thinking := range m.ThinkingBlocks {
		block := map[string]any{"type": thinking.Type}
		switch thinking.Type {
		case "thinking":
			if thinking.Signature == "" {
				return nil, errors.New("anthropic: refusing unsigned thinking block")
			}
			block["thinking"] = thinking.Thinking
			block["signature"] = thinking.Signature
		case "redacted_thinking":
			if thinking.Data == "" {
				return nil, errors.New("anthropic: refusing empty redacted_thinking block")
			}
			block["data"] = thinking.Data
		default:
			return nil, fmt.Errorf("anthropic: unsupported thinking block type %q", thinking.Type)
		}
		items = append(items, anthropicContentItem{index: thinking.Index, indexed: true, seq: seq, block: block})
		seq++
	}
	if m.Content != "" {
		items = append(items, anthropicContentItem{
			index: m.TextBlockIndex, indexed: m.TextBlockIndexed, seq: seq,
			block: map[string]any{"type": "text", "text": m.Content},
		})
		seq++
	}
	for _, tc := range m.ToolCalls {
		input := json.RawMessage(tc.Args)
		if len(input) == 0 || !json.Valid(input) {
			return nil, fmt.Errorf("anthropic: tool %s has invalid JSON arguments", tc.Name)
		}
		items = append(items, anthropicContentItem{
			index: tc.ContentBlockIndex, indexed: tc.ContentBlockIndexed, seq: seq,
			block: map[string]any{"type": "tool_use", "id": tc.ID, "name": tc.Name, "input": input},
		})
		seq++
	}
	if len(items) == 0 {
		items = append(items, anthropicContentItem{block: map[string]any{"type": "text", "text": ""}})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].indexed != items[j].indexed {
			return items[i].indexed
		}
		if items[i].indexed && items[i].index != items[j].index {
			return items[i].index < items[j].index
		}
		return items[i].seq < items[j].seq
	})
	content := make([]any, 0, len(items))
	for _, item := range items {
		content = append(content, item.block)
	}
	if cached {
		// Never mutate a signed thinking block. Cache the final ordinary
		// content block instead; the signed sequence remains byte-for-byte.
		for i := len(items) - 1; i >= 0; i-- {
			if items[i].block["type"] != "thinking" && items[i].block["type"] != "redacted_thinking" {
				items[i].block["cache_control"] = cacheControl()
				break
			}
		}
	}
	return content, nil
}

func applyAnthropicReasoning(body map[string]any, model string, sampling Sampling, maxTokens int) error {
	if !ValidReasoningEffort(sampling.ReasoningEffort) {
		return fmt.Errorf("reasoning effort %q is invalid", sampling.ReasoningEffort)
	}
	effort := NormalizeReasoningEffort(sampling.ReasoningEffort)
	capability := anthropicReasoningCapability(model)
	if !effortAllowed(capability.efforts, effort) {
		return fmt.Errorf("anthropic model %q does not support reasoning effort %q", model, defaultEffortLabel(effort))
	}
	if effort == "" {
		return nil
	}
	switch capability.mode {
	case reasoningAnthropicAdaptive:
		body["thinking"] = map[string]any{"type": "adaptive", "display": "summarized"}
		body["output_config"] = map[string]any{"effort": effort}
	case reasoningAnthropicManual:
		if sampling.Temperature != nil || sampling.TopP != nil || sampling.TopK > 0 {
			return errors.New("anthropic manual thinking cannot be combined with custom temperature, top_p, or top_k")
		}
		budget, err := anthropicManualBudget(maxTokens, effort)
		if err != nil {
			return err
		}
		body["thinking"] = map[string]any{"type": "enabled", "display": "summarized", "budget_tokens": budget}
		if capability.outputEffort {
			body["output_config"] = map[string]any{"effort": effort}
		} else {
			delete(body, "output_config")
		}
	default:
		return fmt.Errorf("anthropic model %q has no supported reasoning wire mode", model)
	}
	return nil
}

func anthropicManualBudget(maxTokens int, effort string) (int, error) {
	if maxTokens <= 1024 {
		return 0, fmt.Errorf("anthropic manual thinking requires max_tokens greater than 1024 (got %d)", maxTokens)
	}
	budget := 1024
	switch effort {
	case "medium":
		budget = maxTokens / 2
	case "high":
		budget = maxTokens * 3 / 4
	case "low":
	default:
		return 0, fmt.Errorf("anthropic manual thinking does not support effort %q", effort)
	}
	if budget < 1024 {
		budget = 1024
	}
	if budget >= maxTokens {
		budget = maxTokens - 1
	}
	return budget, nil
}

func defaultEffortLabel(effort string) string {
	if effort == "" {
		return "auto"
	}
	return effort
}

func (p *anthropicProvider) stream(ctx context.Context, req Request, payload []byte, ch chan<- StreamEvent) {
	defer close(ch)
	u := p.baseURL + "/v1/messages"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		sendStreamEvent(ctx, ch, NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: err.Error()}))
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicVersion)
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
	var usage *Usage
	var pendingTool *toolCall // content_block in progress when it's a tool_use
	var pendingToolArgs strings.Builder
	pendingToolIndex := 0
	toolPayloadBytes := 0
	toolCalls := 0
	completed := false

	emitErr := func(msg string) {
		sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvError, StreamError{Message: msg}))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || !strings.HasPrefix(line, "event:") && !strings.HasPrefix(line, "data:") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		var ev struct {
			Type    string `json:"type"`
			Index   *int   `json:"index"`
			Message struct {
				Usage *struct {
					InputTokens   int `json:"input_tokens"`
					CacheCreation int `json:"cache_creation_input_tokens"`
					CacheRead     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
			ContentBlock *struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
				Data      string `json:"data"`
			} `json:"content_block"`
			Delta *struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			Usage *struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
			Error *struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			emitErr("invalid Anthropic stream event: " + err.Error())
			return
		}
		switch ev.Type {
		case "message_start":
			if ev.Message.Usage != nil {
				usage = &Usage{
					InputTokens:       ev.Message.Usage.InputTokens,
					CacheCreateTokens: ev.Message.Usage.CacheCreation,
					CacheHitTokens:    ev.Message.Usage.CacheRead,
				}
				if err := validateUsage(*usage); err != nil {
					emitErr(err.Error())
					return
				}
			}
		case "content_block_start":
			if pendingTool != nil {
				emitErr("Anthropic started a new content block before the active tool block stopped")
				return
			}
			pendingTool = nil
			pendingToolArgs.Reset()
			if ev.ContentBlock == nil || ev.Index == nil {
				continue
			}
			switch ev.ContentBlock.Type {
			case "tool_use":
				if toolCalls >= providerToolCallLimit {
					emitErr(fmt.Sprintf("Anthropic stream exceeded %d tool calls", providerToolCallLimit))
					return
				}
				metadataBytes := len(ev.ContentBlock.ID) + len(ev.ContentBlock.Name)
				if metadataBytes > providerToolPayloadLimit-toolPayloadBytes {
					emitErr(fmt.Sprintf("Anthropic tool calls exceeded %d-byte payload limit", providerToolPayloadLimit))
					return
				}
				toolCalls++
				toolPayloadBytes += metadataBytes
				pendingTool = &toolCall{ID: ev.ContentBlock.ID, Function: toolFunction{Name: ev.ContentBlock.Name}}
				pendingToolIndex = *ev.Index
			case "thinking":
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, ReasoningDelta{
					Text: ev.ContentBlock.Thinking, Signature: ev.ContentBlock.Signature,
					BlockType: "thinking", Index: *ev.Index, Indexed: true,
				})) {
					return
				}
			case "redacted_thinking":
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, ReasoningDelta{
					Data: ev.ContentBlock.Data, BlockType: "redacted_thinking",
					Index: *ev.Index, Indexed: true,
				})) {
					return
				}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				if ev.Delta.Text != "" {
					td := TextDelta{Text: ev.Delta.Text}
					if ev.Index != nil {
						td.Index, td.Indexed = *ev.Index, true
					}
					if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvTextDelta, td)) {
						return
					}
				}
			case "thinking_delta":
				rd := ReasoningDelta{Text: ev.Delta.Thinking, BlockType: "thinking"}
				if ev.Index != nil {
					rd.Index, rd.Indexed = *ev.Index, true
				}
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, rd)) {
					return
				}
			case "signature_delta":
				rd := ReasoningDelta{Signature: ev.Delta.Signature, BlockType: "thinking"}
				if ev.Index != nil {
					rd.Index, rd.Indexed = *ev.Index, true
				}
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvReasoningDelta, rd)) {
					return
				}
			case "input_json_delta":
				if pendingTool == nil {
					emitErr("Anthropic streamed tool arguments without an active tool block")
					return
				}
				if ev.Index != nil && *ev.Index != pendingToolIndex {
					emitErr("Anthropic streamed tool arguments for the wrong content block")
					return
				}
				if len(ev.Delta.PartialJSON) > providerToolPayloadLimit-toolPayloadBytes {
					emitErr(fmt.Sprintf("Anthropic tool calls exceeded %d-byte payload limit", providerToolPayloadLimit))
					return
				}
				toolPayloadBytes += len(ev.Delta.PartialJSON)
				pendingToolArgs.WriteString(ev.Delta.PartialJSON)
			}
		case "content_block_stop":
			if pendingTool != nil {
				if ev.Index == nil || *ev.Index != pendingToolIndex {
					emitErr("Anthropic stopped the wrong content block while a tool call was active")
					return
				}
				call := ToolCall{ID: pendingTool.ID, Name: pendingTool.Function.Name, Args: pendingToolArgs.String()}
				if ev.Index != nil {
					call.ContentBlockIndex, call.ContentBlockIndexed = *ev.Index, true
				}
				if !sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvToolCall, call)) {
					return
				}
				pendingTool = nil
			}
		case "message_delta":
			if ev.Usage != nil {
				if ev.Usage.OutputTokens < 0 {
					emitErr("Anthropic returned invalid token usage")
					return
				}
				if usage == nil {
					usage = &Usage{}
				}
				usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "message_stop":
			if pendingTool != nil {
				emitErr("Anthropic message stopped before the active tool block completed")
				return
			}
			completed = true
		case "error":
			msg := "unknown error"
			if ev.Error != nil && ev.Error.Message != "" {
				msg = ev.Error.Message
			}
			emitErr(msg)
			return
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, context.Canceled) {
		emitErr(err.Error())
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	if !completed {
		emitErr("Anthropic stream ended before message_stop")
		return
	}
	if usage == nil {
		usage = &Usage{}
	}
	cost, err := p.Cost(req, *usage)
	if err != nil {
		emitErr(err.Error())
		return
	}
	sendStreamEvent(ctx, ch, NewEvent(&seq, RoleOrchestrator, EvDone, Done{Usage: usage, Cost: &cost}))
}
