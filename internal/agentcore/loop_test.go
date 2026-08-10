package agentcore

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTurn is one scripted provider response.
type fakeTurn struct {
	deltas    []string
	reasoning []string
	calls     []ToolCall
	block     bool // block until ctx is done, then emit a cancelled error
}

// fakeProvider scripts turns in order.
type fakeProvider struct {
	mu                sync.Mutex
	turns             []fakeTurn
	call              int
	receivedReasoning string
}

type cancellationAwareProvider struct {
	canceled chan struct{}
}

func (p *cancellationAwareProvider) Name() string                      { return "cancellation-aware" }
func (p *cancellationAwareProvider) Type() string                      { return "fake" }
func (p *cancellationAwareProvider) Models() []Model                   { return nil }
func (p *cancellationAwareProvider) Cost(Request, Usage) (Cost, error) { return Cost{}, nil }
func (p *cancellationAwareProvider) Stream(ctx context.Context, _ Request) (<-chan StreamEvent, error) {
	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		for _, delta := range []string{"1234", "5678", "9ABC"} {
			select {
			case ch <- NewEvent(nil, RoleOrchestrator, EvTextDelta, TextDelta{Text: delta}):
			case <-ctx.Done():
				close(p.canceled)
				return
			}
		}
		<-ctx.Done()
		close(p.canceled)
	}()
	return ch, nil
}

func (f *fakeProvider) Name() string { return "fake" }
func (f *fakeProvider) Type() string { return "fake" }
func (f *fakeProvider) Models() []Model {
	return nil
}
func (f *fakeProvider) Cost(req Request, usage Usage) (Cost, error) { return Cost{}, nil }

func (f *fakeProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	f.mu.Lock()
	idx := f.call
	f.call++
	turn := f.turns[idx]
	var reasoning []string
	for _, m := range req.Messages {
		if m.Reasoning != "" {
			reasoning = append(reasoning, m.Reasoning)
		}
	}
	f.receivedReasoning = strings.Join(reasoning, " ")
	f.mu.Unlock()
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		if turn.block {
			<-ctx.Done()
			ch <- NewEvent(nil, RoleOrchestrator, EvError, StreamError{Message: "cancelled"})
			return
		}
		for _, d := range turn.deltas {
			ch <- NewEvent(nil, RoleOrchestrator, EvTextDelta, TextDelta{Text: d})
		}
		for _, r := range turn.reasoning {
			ch <- NewEvent(nil, RoleOrchestrator, EvReasoningDelta, ReasoningDelta{Text: r})
		}
		for _, c := range turn.calls {
			ch <- NewEvent(nil, RoleOrchestrator, EvToolCall, c)
		}
		ch <- NewEvent(nil, RoleOrchestrator, EvDone, Done{})
	}()
	return ch, nil
}

// echoTool is a Tool that records its args.
type echoTool struct {
	mu   sync.Mutex
	args []map[string]any
}

func (e *echoTool) Spec() ToolSpec {
	return ToolSpec{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}
}

func (e *echoTool) Run(ctx context.Context, args map[string]any) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.args = append(e.args, args)
	return "ok", nil
}

// recordingGate records authorizations; allowAll decides.
type recordingGate struct {
	mu    sync.Mutex
	calls []ToolCall
	allow bool
}

func (g *recordingGate) Authorize(ctx context.Context, call ToolCall, spec ToolSpec) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, call)
	if !g.allow {
		return errors.New("denied by test")
	}
	return nil
}

func newTestLoop(p *fakeProvider, gate Gate) (*Loop, *echoTool, *recordingGate) {
	echo := &echoTool{}
	loop := &Loop{
		Provider: p,
		Model:    "fake-model",
		Tools:    map[string]Tool{"echo": echo},
		Gate:     gate,
	}
	return loop, echo, gate.(*recordingGate)
}

func TestLoopToolRoundTrip(t *testing.T) {
	p := &fakeProvider{turns: []fakeTurn{
		{deltas: []string{"Let me "}, calls: []ToolCall{{ID: "c1", Name: "echo", Args: `{"x":1}`}}},
		{deltas: []string{"Done."}},
	}}
	gate := &recordingGate{allow: true}
	loop, echo, _ := newTestLoop(p, gate)
	var events []StreamEvent
	loop.OnEvent = func(ev StreamEvent) { events = append(events, ev) }

	if err := loop.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// History: user, assistant(with call), tool(result), assistant(final)
	if len(loop.History) != 4 {
		t.Fatalf("history = %d messages: %+v", len(loop.History), loop.History)
	}
	if loop.History[0].Role != "user" || loop.History[0].Content != "hello" {
		t.Errorf("history[0] = %+v", loop.History[0])
	}
	if loop.History[1].Role != "assistant" || len(loop.History[1].ToolCalls) != 1 {
		t.Errorf("history[1] = %+v", loop.History[1])
	}
	if loop.History[2].Role != "tool" || loop.History[2].ToolCallID != "c1" || loop.History[2].Content != "ok" {
		t.Errorf("history[2] = %+v", loop.History[2])
	}
	if loop.History[3].Role != "assistant" || loop.History[3].Content != "Done." {
		t.Errorf("history[3] = %+v", loop.History[3])
	}
	if len(gate.calls) != 1 || gate.calls[0].Name != "echo" {
		t.Errorf("gate calls = %+v", gate.calls)
	}
	if len(echo.args) != 1 || echo.args[0]["x"] != float64(1) {
		t.Errorf("echo args = %+v", echo.args)
	}
	// Events: deltas + one terminal tool result + final delta + done events
	// were emitted. EvToolCall is the running marker; EvToolResult is terminal
	// even when a successful tool has no output.
	var toolResults int
	for _, ev := range events {
		if ev.Type == EvToolResult {
			toolResults++
		}
	}
	if toolResults != 1 {
		t.Errorf("tool result events = %d, want exactly one terminal result", toolResults)
	}
}

func TestLoopGateDenies(t *testing.T) {
	p := &fakeProvider{turns: []fakeTurn{
		{calls: []ToolCall{{ID: "c1", Name: "echo", Args: `{}`}}},
		{deltas: []string{"Fine, skipping."}},
	}}
	gate := &recordingGate{allow: false}
	loop, _, _ := newTestLoop(p, gate)
	if err := loop.Run(context.Background(), "do it"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if loop.History[2].Content != "error: denied by gate: denied by test" {
		t.Errorf("tool result = %q", loop.History[2].Content)
	}
}

func TestLoopUnknownTool(t *testing.T) {
	p := &fakeProvider{turns: []fakeTurn{
		{calls: []ToolCall{{ID: "c1", Name: "nope", Args: `{}`}}},
		{deltas: []string{"ok"}},
	}}
	loop, _, _ := newTestLoop(p, &recordingGate{allow: true})
	if err := loop.Run(context.Background(), "x"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if loop.History[2].Content != "error: unknown tool \"nope\"" {
		t.Errorf("tool result = %q", loop.History[2].Content)
	}
}

func TestLoopMaxTurns(t *testing.T) {
	p := &fakeProvider{turns: []fakeTurn{
		{calls: []ToolCall{{ID: "c1", Name: "echo", Args: `{}`}}},
		{calls: []ToolCall{{ID: "c2", Name: "echo", Args: `{}`}}},
		{calls: []ToolCall{{ID: "c3", Name: "echo", Args: `{}`}}},
		{calls: []ToolCall{{ID: "c4", Name: "echo", Args: `{}`}}}},
	}
	loop, _, _ := newTestLoop(p, &recordingGate{allow: true})
	loop.MaxTurns = 3
	err := loop.Run(context.Background(), "loop")
	if err == nil || !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("Run error = %v, want exceeded", err)
	}
}

func TestLoopCumulativeOutputLimitCancelsBeforeAccumulatingCrossingEvent(t *testing.T) {
	provider := &cancellationAwareProvider{canceled: make(chan struct{})}
	loop := &Loop{
		Provider:       provider,
		Model:          "fake-model",
		Gate:           GateFunc(AllowAll),
		MaxOutputBytes: 10,
	}
	var deltas []string
	loop.OnEvent = func(ev StreamEvent) {
		if ev.Type == EvTextDelta {
			deltas = append(deltas, ev.Content.(TextDelta).Text)
		}
	}
	err := loop.Run(t.Context(), "bounded")
	if err == nil || !strings.Contains(err.Error(), "exceeded 10 bytes") {
		t.Fatalf("Run error = %v, want cumulative output limit", err)
	}
	if got := strings.Join(deltas, ""); got != "12345678" {
		t.Fatalf("emitted deltas = %q, crossing event must be dropped", got)
	}
	if got := loop.LastAssistantText(); got != "" {
		t.Fatalf("partial assistant persisted after failure: %q", got)
	}
	select {
	case <-provider.canceled:
	case <-time.After(time.Second):
		t.Fatal("provider context was not canceled after output limit")
	}
}

func TestLoopOutputLimitRejectsToolResultBeforeEmitOrHistory(t *testing.T) {
	provider := &fakeProvider{turns: []fakeTurn{{calls: []ToolCall{{ID: "c", Name: "huge", Args: `{}`}}}}}
	tool := NewToolFunc(ToolSpec{Name: "huge"}, func(context.Context, map[string]any) (string, error) {
		return strings.Repeat("x", 32), nil
	})
	loop := &Loop{
		Provider: provider, Model: "m", Gate: GateFunc(AllowAll),
		Tools: map[string]Tool{"huge": tool}, MaxOutputBytes: 24,
	}
	var leaked bool
	loop.OnEvent = func(ev StreamEvent) {
		if result, ok := ev.Content.(ToolResult); ok && result.Output != "" {
			leaked = true
		}
	}
	err := loop.Run(t.Context(), "bounded tool")
	if err == nil || !strings.Contains(err.Error(), "exceeded 24 bytes") {
		t.Fatalf("Run error = %v, want tool output limit", err)
	}
	if leaked {
		t.Fatal("oversized tool result was emitted")
	}
	for _, message := range loop.History {
		if message.Role == "tool" {
			t.Fatalf("oversized tool result persisted: %+v", message)
		}
	}
}

func TestStopperCancelsTour(t *testing.T) {
	p := &fakeProvider{turns: []fakeTurn{{block: true}}}
	loop, _, _ := newTestLoop(p, &recordingGate{allow: true})
	stopper := NewStopper()
	loop.Stopper = stopper
	done := make(chan error, 1)
	go func() {
		done <- loop.Run(context.Background(), "blocked")
	}()
	time.Sleep(50 * time.Millisecond)
	stopper.Press() // cancel the tour
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("Run error = %v, want cancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

func TestStopperQuit(t *testing.T) {
	s := NewStopper()
	s.Press() // no tour active → quit
	select {
	case <-s.Quit():
	case <-time.After(time.Second):
		t.Fatal("quit channel not closed")
	}
	s.Press() // second press is a no-op
}

func TestStopperCancelsRunningTool(t *testing.T) {
	started := make(chan struct{})
	tool := NewToolFunc(ToolSpec{Name: "slow"}, func(ctx context.Context, args map[string]any) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	})
	provider := &fakeProvider{turns: []fakeTurn{{calls: []ToolCall{{ID: "1", Name: "slow", Args: `{}`}}}}}
	stopper := NewStopper()
	loop := &Loop{
		Provider: provider, Model: "m", Tools: map[string]Tool{"slow": tool},
		Gate: GateFunc(AllowAll), Stopper: stopper,
	}
	done := make(chan error, 1)
	go func() { done <- loop.Run(t.Context(), "go") }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tool did not start")
	}
	stopper.Press()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("running tool was not cancelled")
	}
}

func TestLoopKeepsReasoningAcrossTurns(t *testing.T) {
	echo := NewToolFunc(ToolSpec{Name: "echo"}, func(ctx context.Context, args map[string]any) (string, error) {
		return "ok", nil
	})
	fp := &fakeProvider{turns: []fakeTurn{
		{
			deltas:    []string{"thinking", "answer"},
			reasoning: []string{"reason 1"},
			calls:     []ToolCall{{ID: "t1", Name: "echo", Args: `{}`}},
		},
		{deltas: []string{"done"}},
	}}
	l, err := Spawn(context.Background(), SpawnOptions{
		Role:     RoleOrchestrator,
		Provider: fp,
		Model:    "m",
		Tools:    map[string]Tool{"echo": echo},
		Gate:     GateFunc(AllowAll),
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if _, err := RunResult(context.Background(), l, "task"); err != nil {
		t.Fatalf("RunResult: %v", err)
	}
	// The second turn must have received the first turn's reasoning back.
	if !strings.Contains(fp.receivedReasoning, "reason 1") {
		t.Errorf("second turn reasoning = %q, want the first turn's reasoning echoed back", fp.receivedReasoning)
	}
}

func TestLoopOwnsMonotonicEventSequence(t *testing.T) {
	var got []uint64
	l := &Loop{OnEvent: func(ev StreamEvent) { got = append(got, ev.Seq) }}
	l.emit(StreamEvent{Seq: 42, Type: EvTextDelta})
	l.emit(StreamEvent{Seq: 1, Type: EvDone})
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("canonical event sequence = %v, want [1 2]", got)
	}
}
