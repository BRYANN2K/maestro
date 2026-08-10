package agentcore

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestStreamStalledWatchdog: a provider that sends one SSE line then stays
// silent must fail the stream with the stall error instead of hanging.
func TestStreamStalledWatchdog(t *testing.T) {
	old := streamIdleTimeout
	streamIdleTimeout = 100 * time.Millisecond
	defer func() { streamIdleTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		if fl != nil {
			fl.Flush()
		}
		// then go silent forever
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := newOpenAI("mock", srv.URL, "sk", false, nil, nil)
	ch, err := p.Stream(context.Background(), Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	gotText := ""
	var gotErr error
	deadline := time.After(5 * time.Second)
loop:
	for gotErr == nil {
		select {
		case ev, ok := <-ch:
			if !ok {
				break loop
			}
			switch ev.Type {
			case EvTextDelta:
				gotText += ev.Content.(TextDelta).Text
			case EvError:
				gotErr = ev.Content.(StreamError)
			}
		case <-deadline:
			t.Fatal("stream did not fail within 5s — watchdog missing")
		}
	}
	if gotText != "hi" {
		t.Errorf("text = %q", gotText)
	}
	if !errors.Is(gotErr, errStreamStalled) && !strings.Contains(gotErr.Error(), "stalled") {
		t.Errorf("error = %v, want the stall error", gotErr)
	}
}

// TestTurnWatchdog: a provider that never emits anything must abort the run
// via the per-turn context deadline.
func TestTurnWatchdog(t *testing.T) {
	fp := &fakeProvider{turns: []fakeTurn{{block: true}}}
	l, err := Spawn(context.Background(), SpawnOptions{
		Role:     RoleOrchestrator,
		Provider: fp,
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
		MaxTurn:  150 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	start := time.Now()
	err = l.Run(context.Background(), "task")
	if err == nil {
		t.Fatal("run must fail when the provider never responds")
	}
	if !strings.Contains(err.Error(), "watchdog") {
		t.Errorf("error = %v, want the turn watchdog message", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("watchdog took too long: %v", time.Since(start))
	}
}
