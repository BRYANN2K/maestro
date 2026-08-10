package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func anthropicStream(t *testing.T, payload string) (*anthropicProvider, <-chan *http.Request) {
	t.Helper()
	reqCh := make(chan *http.Request, 1)
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		reqCh <- r
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, payload)
	})
	return newAnthropic("mock", srv.URL, "sk-ant", []Model{{ID: "claude-sonnet-4", PriceInput: 3, PriceOutput: 15}}), reqCh
}

func TestAnthropicStreamTextToolUsage(t *testing.T) {
	payload := "" +
		"event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" check."}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"main.go\"}"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":1}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	p, reqCh := anthropicStream(t, payload)
	ch, err := p.Stream(context.Background(), Request{Model: "claude-sonnet-4", Messages: []Message{{Role: "user", Content: "read main.go"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	select {
	case req := <-reqCh:
		if req.Header.Get("x-api-key") != "sk-ant" || req.Header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("headers = %+v", req.Header)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server never called")
	}
	evs := collectEvents(t, ch)

	var text strings.Builder
	var calls []ToolCall
	var done *Done
	for _, ev := range evs {
		switch ev.Type {
		case EvTextDelta:
			text.WriteString(ev.Content.(TextDelta).Text)
		case EvToolCall:
			calls = append(calls, ev.Content.(ToolCall))
		case EvDone:
			d := ev.Content.(Done)
			done = &d
		case EvError:
			t.Fatalf("unexpected error: %v", ev.Content)
		}
	}
	if text.String() != "Let me check." {
		t.Errorf("text = %q", text.String())
	}
	if len(calls) != 1 || calls[0].ID != "toolu_1" || calls[0].Name != "read" || calls[0].Args != `{"path":"main.go"}` {
		t.Errorf("calls = %+v", calls)
	}
	if done == nil || done.Usage == nil || done.Usage.InputTokens != 11 || done.Usage.OutputTokens != 7 {
		t.Errorf("done = %+v", done)
	}
}

func decodeAnthropicBody(t *testing.T, p *anthropicProvider, req Request) map[string]any {
	t.Helper()
	data, err := p.buildBody(req)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestAnthropicReasoningUsesModelSpecificWireShape(t *testing.T) {
	p := newAnthropic("custom-name", "https://example.invalid", "sk", nil)
	tests := []struct {
		name, model, effort string
		max                 int
		thinkingType        string
		budget              float64
		outputEffort        bool
	}{
		{"adaptive 4.6", "claude-sonnet-4-6", "high", 8_000, "adaptive", 0, true},
		{"manual opus 4.5", "claude-opus-4-5-20251101", "medium", 8_000, "enabled", 4_000, true},
		{"manual sonnet 4.5", "claude-sonnet-4-5", "low", 4_096, "enabled", 1_024, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := decodeAnthropicBody(t, p, Request{Model: tt.model, Sampling: Sampling{ReasoningEffort: tt.effort, MaxTokens: tt.max}})
			thinking, ok := body["thinking"].(map[string]any)
			if !ok || thinking["type"] != tt.thinkingType || thinking["display"] != "summarized" {
				t.Fatalf("thinking = %#v", body["thinking"])
			}
			if tt.budget == 0 {
				if _, exists := thinking["budget_tokens"]; exists {
					t.Fatalf("adaptive body has budget_tokens: %v", thinking)
				}
			} else if thinking["budget_tokens"] != tt.budget {
				t.Fatalf("budget_tokens = %v, want %v", thinking["budget_tokens"], tt.budget)
			}
			output, hasOutput := body["output_config"].(map[string]any)
			if hasOutput != tt.outputEffort {
				t.Fatalf("output_config present = %v, want %v (%v)", hasOutput, tt.outputEffort, body)
			}
			if hasOutput && output["effort"] != tt.effort {
				t.Fatalf("output effort = %v", output)
			}
		})
	}
}

func TestAnthropicReasoningFailsClosedForUnknownOrUnsupportedModel(t *testing.T) {
	p := newAnthropic("anthropic", "https://example.invalid", "sk", nil)
	for _, req := range []Request{
		{Model: "claude-future-unknown", Sampling: Sampling{ReasoningEffort: "high"}},
		{Model: "claude-sonnet-4-6", Sampling: Sampling{ReasoningEffort: "xhigh"}},
		{Model: "claude-opus-4-5", Sampling: Sampling{ReasoningEffort: "high", MaxTokens: 1024}},
	} {
		if _, err := p.buildBody(req); err == nil {
			t.Fatalf("buildBody(%+v) unexpectedly succeeded", req)
		}
	}
	body := decodeAnthropicBody(t, p, Request{Model: "claude-future-unknown"})
	if _, exists := body["thinking"]; exists {
		t.Fatalf("automatic unknown model gained thinking config: %v", body)
	}
}

func TestAnthropicPreservesRedactedThinkingBlock(t *testing.T) {
	p := newAnthropic("anthropic", "https://example.invalid", "sk", nil)
	body := decodeAnthropicBody(t, p, Request{
		Model: "claude-sonnet-4-6",
		Messages: []Message{{Role: "assistant", ThinkingBlocks: []ThinkingBlock{{
			Type: "redacted_thinking", Data: "encrypted-redaction", Index: 0,
		}}, ToolCalls: []ToolCall{{
			ID: "toolu_1", Name: "read", Args: `{}`, ContentBlockIndex: 1, ContentBlockIndexed: true,
		}}}},
	})
	messages := body["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	redacted := content[0].(map[string]any)
	if len(redacted) != 2 || redacted["type"] != "redacted_thinking" || redacted["data"] != "encrypted-redaction" {
		t.Fatalf("redacted block was modified: %#v", redacted)
	}
}

func TestAnthropicLoopPreservesSignedThinkingAcrossToolTurn(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	call := 0
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		mu.Lock()
		bodies = append(bodies, body)
		call++
		current := call
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if current == 1 {
			fmt.Fprint(w, "event: content_block_start\n"+
				`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`+"\n\n"+
				"event: content_block_delta\n"+
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"check carefully"}}`+"\n\n"+
				"event: content_block_delta\n"+
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"signed-token"}}`+"\n\n"+
				"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n"+
				"event: content_block_start\n"+
				`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"read"}}`+"\n\n"+
				"event: content_block_delta\n"+
				`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`+"\n\n"+
				"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":1}`+"\n\n"+
				"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
			return
		}
		fmt.Fprint(w, "event: content_block_start\n"+
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n"+
			"event: content_block_delta\n"+
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`+"\n\n"+
			"event: content_block_stop\n"+`data: {"type":"content_block_stop","index":0}`+"\n\n"+
			"event: message_stop\n"+`data: {"type":"message_stop"}`+"\n\n")
	})
	p := newAnthropic("custom", srv.URL, "sk", []Model{{ID: "claude-sonnet-4-6", CanReason: true}})
	loop := &Loop{
		Provider: p, Model: "claude-sonnet-4-6", Sampling: Sampling{ReasoningEffort: "high", MaxTokens: 8_000},
		Tools: map[string]Tool{"read": NewToolFunc(ToolSpec{Name: "read"}, func(context.Context, map[string]any) (string, error) { return "ok", nil })},
		Gate:  GateFunc(AllowAll),
	}
	if err := loop.Run(t.Context(), "inspect"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("request count = %d", len(bodies))
	}
	messages := bodies[1]["messages"].([]any)
	assistant := messages[1].(map[string]any)
	content := assistant["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content = %#v", content)
	}
	thinking := content[0].(map[string]any)
	if len(thinking) != 3 || thinking["type"] != "thinking" || thinking["thinking"] != "check carefully" || thinking["signature"] != "signed-token" {
		t.Fatalf("thinking block was modified: %#v", thinking)
	}
	if tool := content[1].(map[string]any); tool["type"] != "tool_use" || tool["id"] != "toolu_1" {
		t.Fatalf("tool block order = %#v", tool)
	}
}

func TestAnthropicTruncatedStreamFailsClosed(t *testing.T) {
	p, _ := anthropicStream(t, "event: content_block_delta\n"+
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`+"\n\n")
	ch, err := p.Stream(t.Context(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, ch)
	seenError, seenDone := false, false
	for _, event := range events {
		seenError = seenError || event.Type == EvError
		seenDone = seenDone || event.Type == EvDone
	}
	if !seenError || seenDone {
		t.Fatalf("truncated events = %+v; want error without done", events)
	}
}

func TestAnthropicStreamBody(t *testing.T) {
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\n\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	p := newAnthropic("mock", srv.URL, "sk", nil)
	var gotBody map[string]any
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\n\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	ch, err := p.Stream(context.Background(), Request{
		Model:    "m",
		System:   []Message{{Role: "system", Content: "be brief"}},
		Messages: []Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "yo", ToolCalls: []ToolCall{{ID: "t1", Name: "read", Args: `{"path":"x"}`}}}},
		Sampling: Sampling{Temperature: floatPtr(0.5), MaxTokens: 100},
		Tools:    []ToolSpec{{Name: "read", Description: "Read", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, ch)
	if gotBody["model"] != "m" || gotBody["max_tokens"] != float64(100) || gotBody["temperature"] != 0.5 {
		t.Errorf("body = %v", gotBody)
	}
	if sys, ok := gotBody["system"].([]any); !ok || len(sys) != 1 {
		t.Errorf("system = %v", gotBody["system"])
	} else if block, ok := sys[0].(map[string]any); !ok || block["type"] != "text" || block["text"] != "be brief" {
		t.Errorf("system[0] = %v", sys[0])
	}
	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages = %v", gotBody["messages"])
	}
	assistant := msgs[1].(map[string]any)
	content := assistant["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("assistant content = %v", content)
	}
	toolUse := content[1].(map[string]any)
	if toolUse["type"] != "tool_use" || toolUse["id"] != "t1" || toolUse["name"] != "read" {
		t.Errorf("tool_use = %v", toolUse)
	}
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Errorf("tools = %v", gotBody["tools"])
	}
}

func bodyCacheFlags(t *testing.T, req Request) map[string]any {
	t.Helper()
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\n\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	p := newAnthropic("mock", srv.URL, "sk", nil)
	var gotBody map[string]any
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_stop\n\ndata: {\"type\":\"message_stop\"}\n\n")
	})
	req.Model = "m"
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, ch)
	return gotBody
}

func hasCacheControl(t *testing.T, m map[string]any) bool {
	t.Helper()
	cc, ok := m["cache_control"].(map[string]any)
	return ok && cc["type"] == "ephemeral"
}

func TestAnthropicBodyPromptCache(t *testing.T) {
	old := cacheOn
	defer func() { cacheOn = old }()
	cacheOn = true

	body := bodyCacheFlags(t, Request{
		System:   []Message{{Role: "system", Content: "sys"}},
		Messages: []Message{{Role: "user", Content: "m1"}, {Role: "user", Content: "m2"}, {Role: "user", Content: "m3"}, {Role: "user", Content: "m4"}, {Role: "user", Content: "m5"}},
		Tools:    []ToolSpec{{Name: "read", Description: "R"}, {Name: "write", Description: "W"}},
	})
	if sys, ok := body["system"].([]any); !ok || len(sys) != 1 {
		t.Fatalf("system = %v", body["system"])
	} else if !hasCacheControl(t, sys[0].(map[string]any)) {
		t.Error("system block missing cache_control")
	}
	msgs := body["messages"].([]any)
	for i, raw := range msgs {
		m := raw.(map[string]any)
		content := m["content"].([]any)
		block := content[0].(map[string]any)
		want := i >= len(msgs)-3
		if hasCacheControl(t, block) != want {
			t.Errorf("msg %d: cache_control present=%v, want %v (block %v)", i, hasCacheControl(t, block), want, block)
		}
	}
	tools := body["tools"].([]any)
	if hasCacheControl(t, tools[0].(map[string]any)) {
		t.Error("first tool must not be cached")
	}
	if !hasCacheControl(t, tools[1].(map[string]any)) {
		t.Error("last tool must carry cache_control")
	}
}

func TestAnthropicBodyPromptCacheDisabled(t *testing.T) {
	old := cacheOn
	defer func() { cacheOn = old }()
	cacheOn = false

	body := bodyCacheFlags(t, Request{
		System:   []Message{{Role: "system", Content: "sys"}},
		Messages: []Message{{Role: "user", Content: "m1"}, {Role: "user", Content: "m2"}},
		Tools:    []ToolSpec{{Name: "read", Description: "R"}},
	})
	if sys, ok := body["system"].([]any); !ok || hasCacheControl(t, sys[0].(map[string]any)) {
		t.Errorf("cache_control must be absent when disabled: %v", body["system"])
	}
	for _, raw := range body["messages"].([]any) {
		m := raw.(map[string]any)
		block := m["content"].([]any)[0].(map[string]any)
		if hasCacheControl(t, block) {
			t.Errorf("msg cache_control when disabled: %v", block)
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		if hasCacheControl(t, tools[0].(map[string]any)) {
			t.Errorf("tool cache_control when disabled: %v", tools[0])
		}
	}
}

func TestAnthropicStreamCacheUsage(t *testing.T) {
	payload := "" +
		"event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":11,"cache_creation_input_tokens":4,"cache_read_input_tokens":7}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":2}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	p, _ := anthropicStream(t, payload)
	ch, err := p.Stream(context.Background(), Request{Model: "claude-sonnet-4", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)
	for _, ev := range evs {
		if ev.Type == EvDone {
			d := ev.Content.(Done)
			if d.Usage == nil || d.Usage.CacheCreateTokens != 4 || d.Usage.CacheHitTokens != 7 {
				t.Errorf("usage = %+v", d.Usage)
			}
			return
		}
		if ev.Type == EvError {
			t.Fatalf("error: %v", ev.Content)
		}
	}
	t.Fatal("no done event")
}

func TestAnthropicStreamErrorEvent(t *testing.T) {
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: error\n\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	})
	p := newAnthropic("mock", srv.URL, "sk", nil)
	ch, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)
	var msg string
	for _, ev := range evs {
		if ev.Type == EvError {
			msg = ev.Content.(StreamError).Message
		}
	}
	if !strings.Contains(msg, "Overloaded") {
		t.Errorf("error = %q", msg)
	}
}

func TestAnthropicStreamMissingKey(t *testing.T) {
	p := newAnthropic("mock", "http://localhost:1", "", nil)
	if _, err := p.Stream(context.Background(), Request{Model: "m"}); err == nil {
		t.Error("Stream without key should fail")
	}
}

func TestAnthropicCost(t *testing.T) {
	p := newAnthropic("mock", "", "sk", []Model{{ID: "claude-sonnet-4", PriceInput: 3, PriceOutput: 15}})
	cost, err := p.Cost(Request{Model: "claude-sonnet-4"}, Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if err != nil || cost.InputUSD != 3 || cost.OutputUSD != 15 {
		t.Errorf("cost = %+v, %v", cost, err)
	}
}
