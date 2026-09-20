package agentcore

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	if done.Usage.InputTokens != 5 || done.Usage.OutputTokens != 5 || done.Usage.CacheCreateTokens != 2 || done.Usage.CacheHitTokens != 3 {
		t.Errorf("usage = %+v", done.Usage)
	}
	if totalInput := done.Usage.InputTokens + done.Usage.CacheCreateTokens + done.Usage.CacheHitTokens; totalInput != 10 {
		t.Errorf("normalized prompt tokens = %d, want provider total 10", totalInput)
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

func TestOpenAIStreamPreservesStaticThenDynamicSystemOrder(t *testing.T) {
	var gotBody map[string]any
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk-test", false, nil, nil)
	ch, err := p.Stream(t.Context(), Request{
		Model: "m",
		System: []Message{
			{Role: "system", Content: "STATIC-FIRST"},
			{Role: "system", Content: "STATIC-SECOND"},
		},
		Messages: []Message{
			{Role: "system", Content: "DYNAMIC-THIRD"},
			{Role: "user", Content: "USER-LAST"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	collectEvents(t, ch)

	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) != 4 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	want := []struct{ role, content string }{
		{"system", "STATIC-FIRST"},
		{"system", "STATIC-SECOND"},
		{"system", "DYNAMIC-THIRD"},
		{"user", "USER-LAST"},
	}
	for i, expected := range want {
		message := messages[i].(map[string]any)
		if message["role"] != expected.role || message["content"] != expected.content {
			t.Errorf("message %d = %#v, want role=%q content=%q", i, message, expected.role, expected.content)
		}
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

func TestOpenAIBodyRejectsReservedProviderOption(t *testing.T) {
	p := newOpenAI("mock", "http://example.invalid", "sk-test", false, nil, nil)
	_, err := p.buildBody(Request{
		Model: "m",
		Sampling: Sampling{ProviderOptions: map[string]any{
			"max_tokens": 999_999,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), `provider option "max_tokens" is reserved`) {
		t.Fatalf("reserved provider option error = %v", err)
	}
}

func TestOpenAIStreamToolCallFragments(t *testing.T) {
	chunk1 := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_","function":{"name":"re","arguments":"{\"path\":\""}}]}}]}`
	chunk2 := `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"ad","arguments":"main.go\"}"}}]}}]}`
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

func TestOpenAIStreamBoundsAccumulatedToolPayload(t *testing.T) {
	fragment := strings.Repeat("x", 512<<10)
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < providerToolPayloadLimit/len(fragment)+1; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"write\",\"arguments\":%q}}]}}]}\n\n", fragment)
		}
	})
	p := newOpenAI("mock", srv.URL, "sk-test", false, nil, nil)
	ch, err := p.Stream(t.Context(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "write"}}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, ch)
	seenLimit := false
	for _, event := range events {
		if event.Type == EvToolCall || event.Type == EvDone {
			t.Fatalf("oversize tool payload emitted terminal event: %+v", event)
		}
		if event.Type == EvError && strings.Contains(event.Content.(StreamError).Message, "payload limit") {
			seenLimit = true
		}
	}
	if !seenLimit {
		t.Fatalf("events = %+v, want tool payload limit error", events)
	}
}

func TestOpenAIStreamCancellationReleasesFullEventChannel(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < 256; i++ {
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		}
	})
	p := newOpenAI("mock", srv.URL, "sk-test", false, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	ch, err := p.Stream(ctx, Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(ch) < cap(ch) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(ch) < cap(ch) {
		t.Fatalf("stream never filled its event channel: %d/%d", len(ch), cap(ch))
	}
	cancel()
	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled OpenAI stream remained blocked on event delivery")
	}
}

func TestOpenAIStreamRejectsInvalidUsage(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"prompt_tokens_details\":{\"cached_tokens\":3}}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
	p := newOpenAI("mock", srv.URL, "sk-test", false, nil, nil)
	ch, err := p.Stream(t.Context(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	events := collectEvents(t, ch)
	if len(events) != 1 || events[0].Type != EvError || !strings.Contains(events[0].Content.(StreamError).Message, "invalid token usage") {
		t.Fatalf("invalid usage events = %+v", events)
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
	t.Cleanup(func() { _ = p.Close() })

	// The triggering call is a static snapshot, even when the server can reply
	// immediately. Discovery is deliberately never on the caller's path.
	if models := p.Models(); len(models) != 1 || models[0].ID != "static-model" {
		t.Fatalf("initial models = %+v, want static snapshot", models)
	}
	models := waitForOpenAIModels(t, p, time.Second, "static-model", "gpt-4o", "gpt-4o-mini")
	if len(models) != 3 {
		t.Errorf("published models = %+v, want three unique models", models)
	}
}

func TestOpenAIDiscoveryFailureIsSilent(t *testing.T) {
	requestDone := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		defer close(requestDone)
		w.WriteHeader(http.StatusInternalServerError)
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static"}})
	t.Cleanup(func() { _ = p.Close() })
	models := p.Models()
	if len(models) != 1 || models[0].ID != "static" {
		t.Errorf("models = %+v, want static only", models)
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("discovery request did not finish")
	}
	waitForOpenAIDiscoveryIdle(t, p, time.Second)
	if models := p.Models(); len(models) != 1 || models[0].ID != "static" {
		t.Errorf("models after failure = %+v, want static only", models)
	}
}

func TestOpenAIDiscoveryModelsDoesNotWaitForSlowDrip(t *testing.T) {
	started := make(chan struct{})
	exited := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		defer close(exited)
		flusher, _ := w.(http.Flusher)
		for _, b := range []byte(`{"data":[{"id":"too-slow"}]}`) {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
			_, _ = w.Write([]byte{b})
			if flusher != nil {
				flusher.Flush()
			}
		}
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static"}})
	p.discoveryTimeout = 75 * time.Millisecond
	t.Cleanup(func() { _ = p.Close() })

	start := time.Now()
	models := p.Models()
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("Models blocked for %v on live discovery", elapsed)
	}
	if len(models) != 1 || models[0].ID != "static" {
		t.Fatalf("initial models = %+v, want static snapshot", models)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("slow discovery request did not start")
	}
	waitForOpenAIDiscoveryIdle(t, p, time.Second)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("timed-out discovery did not close its response")
	}
	if models := p.Models(); len(models) != 1 || models[0].ID != "static" {
		t.Errorf("models after timeout = %+v, want static only", models)
	}
}

func TestOpenAIDiscoveryRejectsOversizeBody(t *testing.T) {
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"data":[{"id":%q}]}`, strings.Repeat("x", 512))
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static"}})
	p.discoveryResponseMaxBody = 64
	t.Cleanup(func() { _ = p.Close() })

	p.Models()
	waitForOpenAIDiscoveryIdle(t, p, time.Second)
	p.mu.RLock()
	done := p.discoveryDone
	p.mu.RUnlock()
	if done {
		t.Fatal("oversize discovery response was published")
	}
	if models := p.Models(); len(models) != 1 || models[0].ID != "static" {
		t.Errorf("models after oversize response = %+v, want static only", models)
	}
}

func TestOpenAIDiscoveryConcurrentCallersSingleFetch(t *testing.T) {
	var requests atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
			fmt.Fprint(w, `{"data":[{"id":"live"}]}`)
		case <-r.Context().Done():
		}
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static"}})
	t.Cleanup(func() { _ = p.Close() })

	const callers = 64
	ready := make(chan struct{})
	results := make(chan []Model, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-ready
			results <- p.Models()
		}()
	}
	close(ready)
	wg.Wait()
	close(results)
	for models := range results {
		if len(models) != 1 || models[0].ID != "static" {
			t.Fatalf("concurrent snapshot = %+v, want static only", models)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("discovery request did not start")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("discovery requests = %d, want one", got)
	}
	close(release)
	waitForOpenAIModels(t, p, time.Second, "static", "live")
	if got := requests.Load(); got != 1 {
		t.Fatalf("discovery requests after publication = %d, want one", got)
	}
}

func TestOpenAIDiscoveryRetriesAfterCooldown(t *testing.T) {
	var requests atomic.Int32
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"recovered"}]}`)
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static"}})
	p.discoveryRetryCooldown = 50 * time.Millisecond
	t.Cleanup(func() { _ = p.Close() })

	p.Models()
	waitForOpenAIDiscoveryIdle(t, p, time.Second)
	for range 32 {
		p.Models()
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests during cooldown = %d, want one", got)
	}
	time.Sleep(p.discoveryRetryCooldown)
	waitForOpenAIModels(t, p, time.Second, "static", "recovered")
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests after recovery = %d, want two", got)
	}
}

func TestOpenAIDiscoveryCloseCancelsAndWaits(t *testing.T) {
	started := make(chan struct{})
	handlerExited := make(chan struct{})
	srv := sseServer(t, func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(handlerExited)
	})
	p := newOpenAI("mock", srv.URL, "sk", true, nil, []Model{{ID: "static"}})
	p.discoveryTimeout = time.Hour
	t.Cleanup(func() { _ = p.Close() })
	p.Models()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("discovery request did not start")
	}

	closed := make(chan struct{})
	go func() {
		_ = p.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for cancelled discovery to stop")
	}
	select {
	case <-handlerExited:
	case <-time.After(time.Second):
		t.Fatal("cancelled discovery handler did not exit")
	}
	// Close is idempotent, and a closed provider never starts discovery again.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if models := p.Models(); len(models) != 1 || models[0].ID != "static" {
		t.Fatalf("models after Close = %+v, want static snapshot", models)
	}
}

func waitForOpenAIModels(t *testing.T, p *openaiProvider, timeout time.Duration, want ...string) []Model {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		models := p.Models()
		ids := make(map[string]bool, len(models))
		for _, model := range models {
			ids[model.ID] = true
		}
		matched := true
		for _, id := range want {
			matched = matched && ids[id]
		}
		if matched {
			return models
		}
		if time.Now().After(deadline) {
			t.Fatalf("models = %+v; did not publish %v within %v", models, want, timeout)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitForOpenAIDiscoveryIdle(t *testing.T, p *openaiProvider, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		p.mu.RLock()
		inFlight := p.discoveryInFlight
		p.mu.RUnlock()
		if !inFlight {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovery still running after %v", timeout)
		}
		time.Sleep(time.Millisecond)
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
