package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sseServer starts a server that calls handler per request. The handler
// writes raw SSE to w.
func sseServer(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func collectEvents(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var evs []StreamEvent
	for ev := range ch {
		evs = append(evs, ev)
	}
	return evs
}

func TestOpenAIStreamTextAndUsage(t *testing.T) {
	var gotBody map[string]any
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" there!\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cache_creation\":2,\"cached_tokens\":3}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk-test", false, nil, []Model{{ID: "gpt-4o", PriceInput: 3, PriceOutput: 15}})
	ch, err := p.Stream(context.Background(), Request{
		Model:    "gpt-4o",
		System:   []Message{{Role: "system", Content: "be brief"}},
		Messages: []Message{{Role: "user", Content: "hi"}},
		Sampling: Sampling{Temperature: floatPtr(0.2), MaxTokens: 100, ReasoningEffort: "xhigh"},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)

	var text strings.Builder
	var done *Done
	for _, ev := range evs {
		switch ev.Type {
		case EvTextDelta:
			text.WriteString(ev.Content.(TextDelta).Text)
		case EvDone:
			d := ev.Content.(Done)
			done = &d
		case EvError:
			t.Fatalf("unexpected error event: %v", ev.Content)
		}
	}
	if text.String() != "Hello there!" {
		t.Errorf("text = %q", text.String())
	}
	if done == nil || done.Usage == nil {
		t.Fatal("missing done event with usage")
	}
	if done.Usage.InputTokens != 10 || done.Usage.OutputTokens != 5 || done.Usage.CacheCreateTokens != 2 || done.Usage.CacheHitTokens != 3 {
		t.Errorf("usage = %+v", done.Usage)
	}
	if done.Cost == nil || done.Cost.Total() <= 0 {
		t.Errorf("cost = %+v", done.Cost)
	}
	if gotBody["model"] != "gpt-4o" || gotBody["stream"] != true || gotBody["max_tokens"] != float64(100) || gotBody["temperature"] != 0.2 {
		t.Errorf("request body = %v", gotBody)
	}
	if gotBody["reasoning_effort"] != "xhigh" {
		t.Errorf("reasoning_effort = %v, want xhigh", gotBody["reasoning_effort"])
	}
	if msgs, ok := gotBody["messages"].([]any); !ok || len(msgs) != 2 {
		t.Errorf("messages = %v", gotBody["messages"])
	}
}

func TestOpenAIBodyOmitsAutomaticAndRejectsUnknownReasoning(t *testing.T) {
	p := newOpenAI("mock", "http://example.invalid", "sk-test", false, nil, nil)
	body, err := p.buildBody(Request{Model: "m", Sampling: Sampling{ReasoningEffort: "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["reasoning_effort"]; ok {
		t.Fatalf("automatic reasoning leaked into body: %s", body)
	}
	if _, err := p.buildBody(Request{Model: "m", Sampling: Sampling{ReasoningEffort: "ultra"}}); err == nil {
		t.Fatal("unknown reasoning effort accepted")
	}
}

func TestOpenAIStreamToolCallFragments(t *testing.T) {
	chunk1 := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read","arguments":"{\"path\":\""}}]}}]}`
	chunk2 := `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"main.go\"}"}}]}}]}`
	chunk3 := `{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "data: %s\n\n", chunk1)
		fmt.Fprintf(w, "data: %s\n\n", chunk2)
		fmt.Fprintf(w, "data: %s\n\n", chunk3)
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk-test", false, nil, nil)
	ch, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "read"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)
	var calls []ToolCall
	for _, ev := range evs {
		if ev.Type == EvToolCall {
			calls = append(calls, ev.Content.(ToolCall))
		}
	}
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1: %+v", len(calls), calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "read" || calls[0].Args != `{"path":"main.go"}` {
		t.Errorf("tool call = %+v", calls[0])
	}
}

func TestOpenAIStreamHTTPError(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
	})
	p := newOpenAI("mock", srv.URL, "sk-bad", false, nil, nil)
	ch, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	evs := collectEvents(t, ch)
	var errMsg string
	for _, ev := range evs {
		if ev.Type == EvError {
			errMsg = ev.Content.(StreamError).Message
		}
	}
	if !strings.Contains(errMsg, "401") {
		t.Errorf("error message = %q, want 401", errMsg)
	}
}

func TestOpenAIStreamMissingKey(t *testing.T) {
	p := newOpenAI("mock", "http://localhost:1", "", false, nil, nil)
	if _, err := p.Stream(context.Background(), Request{Model: "m"}); err == nil {
		t.Error("Stream without key should fail")
	}
}

func TestOpenAITruncatedStreamFailsClosed(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk-test", false, nil, nil)
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

func TestOpenAIDiscovery(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/models") {
			t.Errorf("discovery path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"gpt-4o"},{"id":"gpt-4o-mini"}]}`)
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static-model"}})
	models := p.Models()
	ids := map[string]bool{}
	for _, m := range models {
		ids[m.ID] = true
	}
	if !ids["gpt-4o"] || !ids["gpt-4o-mini"] || !ids["static-model"] {
		t.Errorf("discovered models = %v", ids)
	}
}

func TestOpenAIDiscoveryFailureIsSilent(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static"}})
	models := p.Models()
	if len(models) != 1 || models[0].ID != "static" {
		t.Errorf("models = %+v, want static only", models)
	}
}

func TestOpenAICost(t *testing.T) {
	p := newOpenAI("mock", "http://x", "sk", false, nil, []Model{{ID: "m", PriceInput: 3, PriceOutput: 15}})
	cost, err := p.Cost(Request{Model: "m"}, Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if err != nil {
		t.Fatalf("Cost: %v", err)
	}
	if cost.InputUSD != 3 || cost.OutputUSD != 15 {
		t.Errorf("cost = %+v", cost)
	}
	cost, _ = p.Cost(Request{Model: "unknown"}, Usage{})
	if cost.Total() != 0 {
		t.Errorf("unknown model cost = %+v, want zero", cost)
	}
}

func TestOpenAIToolsInBody(t *testing.T) {
	var gotBody map[string]any
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk", false, nil, nil)
	ch, err := p.Stream(context.Background(), Request{
		Model:    "m",
		Messages: []Message{{Role: "user", Content: "hi"}},
		Tools:    []ToolSpec{{Name: "read", Description: "Read a file", InputSchema: map[string]any{"type": "object"}}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	collectEvents(t, ch)
	tools, ok := gotBody["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %v", gotBody["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["function"].(map[string]any)["name"] != "read" {
		t.Errorf("tool = %v", tool)
	}
}

func floatPtr(f float64) *float64 { return &f }

func TestOpenAIStreamReasoningDelta(t *testing.T) {
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"let me think\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk", false, nil, nil)
	ch, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning, text strings.Builder
	for ev := range ch {
		switch ev.Type {
		case EvReasoningDelta:
			reasoning.WriteString(ev.Content.(ReasoningDelta).Text)
		case EvTextDelta:
			text.WriteString(ev.Content.(TextDelta).Text)
		case EvError:
			t.Fatalf("error: %v", ev.Content)
		}
	}
	if reasoning.String() != "let me think" {
		t.Errorf("reasoning = %q", reasoning.String())
	}
	if text.String() != "hello" {
		t.Errorf("text = %q", text.String())
	}
}

func TestOpenAIBodySendsReasoningBack(t *testing.T) {
	var gotBody map[string]any
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk", false, nil, nil)
	ch, err := p.Stream(context.Background(), Request{
		Model: "m",
		Messages: []Message{
			{Role: "assistant", Content: "answer", Reasoning: "think step 1"},
			{Role: "user", Content: "next"},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range ch {
	}
	msgs := gotBody["messages"].([]any)
	assistant := msgs[0].(map[string]any)
	if assistant["reasoning_content"] != "think step 1" {
		t.Errorf("reasoning_content missing from assistant message: %v", assistant)
	}
	if _, ok := assistant["reasoning_content"]; !ok {
		t.Error("reasoning_content field absent")
	}
}
