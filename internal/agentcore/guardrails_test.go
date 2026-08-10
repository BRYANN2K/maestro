package agentcore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeTurnExt extends fakeTurn with a violating delta.
type ruleTurn struct {
	deltas []string
	block  bool
}

// ruleProvider streams scripted turns, one per call.
type ruleProvider struct {
	turns []ruleTurn
	call  int
}

func (f *ruleProvider) Name() string { return "rule" }
func (f *ruleProvider) Type() string { return "rule" }
func (f *ruleProvider) Models() []Model {
	return nil
}
func (f *ruleProvider) Cost(req Request, usage Usage) (Cost, error) { return Cost{}, nil }

func (f *ruleProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	turn := f.turns[f.call]
	f.call++
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
		ch <- NewEvent(nil, RoleOrchestrator, EvDone, Done{})
	}()
	return ch, nil
}

// providerWithCalls emits a scripted set of tool calls then text; with
// onlyFirst set, the calls are emitted only on the first provider call.
type callProvider struct {
	calls     []ToolCall
	after     []string
	onlyFirst bool
	call      int
}

func (f *callProvider) Name() string { return "calls" }
func (f *callProvider) Type() string { return "calls" }
func (f *callProvider) Models() []Model {
	return nil
}
func (f *callProvider) Cost(req Request, usage Usage) (Cost, error) { return Cost{}, nil }

func (f *callProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	f.call++
	ch := make(chan StreamEvent, 16)
	go func() {
		defer close(ch)
		if f.onlyFirst && f.call > 1 {
			for _, d := range f.after {
				ch <- NewEvent(nil, RoleOrchestrator, EvTextDelta, TextDelta{Text: d})
			}
			ch <- NewEvent(nil, RoleOrchestrator, EvDone, Done{})
			return
		}
		for _, c := range f.calls {
			ch <- NewEvent(nil, RoleOrchestrator, EvToolCall, c)
		}
		for _, d := range f.after {
			ch <- NewEvent(nil, RoleOrchestrator, EvTextDelta, TextDelta{Text: d})
		}
		ch <- NewEvent(nil, RoleOrchestrator, EvDone, Done{})
	}()
	return ch, nil
}

func TestLoopRulesInterruptAndResume(t *testing.T) {
	rs, err := CompileRules("## Stream Rules\n- forbid: `panic\\(`\n  because: Never panic.\n")
	if err != nil {
		t.Fatalf("CompileRules: %v", err)
	}
	p := &ruleProvider{turns: []ruleTurn{
		{deltas: []string{"the code uses ", "panic( everywhere"}}, // violates mid-token
		{deltas: []string{"the code is clean"}},                   // resumed after injection
	}}
	loop := &Loop{
		Provider: p,
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
		Rules:    rs,
	}
	var notes []string
	loop.OnEvent = func(ev StreamEvent) {
		if ev.Type == EvAdvisorNote {
			if n, ok := ev.Content.(AdvisorNote); ok {
				notes = append(notes, n.Note)
			}
		}
	}
	if err := loop.Run(context.Background(), "write the code"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	// The violating text must NOT appear in the final assistant reply.
	last := loop.LastAssistantText()
	if strings.Contains(last, "panic(") {
		t.Errorf("violating text leaked into the reply: %q", last)
	}
	if !strings.Contains(last, "clean") {
		t.Errorf("resumed reply = %q", last)
	}
	// The reminder was injected as a system message (survives compaction).
	found := false
	for _, msg := range loop.History {
		if msg.Role == "system" && strings.Contains(msg.Content, "Never panic") {
			found = true
		}
	}
	if !found {
		t.Errorf("reminder missing from history: %+v", loop.History)
	}
	if len(notes) != 1 {
		t.Errorf("advisor notes = %v", notes)
	}
	if rs.Fired() != 1 {
		t.Errorf("fired = %d", rs.Fired())
	}
}

func TestLoopConformingTurnNoTax(t *testing.T) {
	rs, _ := CompileRules("## Stream Rules\n- forbid: `panic\\(`\n")
	p := &ruleProvider{turns: []ruleTurn{{deltas: []string{"clean code"}}}}
	loop := &Loop{
		Provider: p,
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
		Rules:    rs,
	}
	if err := loop.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rs.Fired() != 0 {
		t.Errorf("conforming turn must not fire rules, fired = %d", rs.Fired())
	}
	if len(loop.History) != 2 { // user + assistant only, no injection
		t.Errorf("history = %d messages", len(loop.History))
	}
}

func TestBudgetKillSwitch(t *testing.T) {
	b := NewBudgetState(Budget{MaxUSD: 1.0}, 0)
	ctx, cancel := context.WithCancel(context.Background())
	b.SetKill(cancel)
	defer cancel()

	kill, alert := b.Track(NewEvent(nil, RoleOrchestrator, EvDone, Done{Cost: &Cost{InputUSD: 1.5}}))
	if !kill {
		t.Fatal("budget cap should kill the run")
	}
	if alert {
		t.Error("80% alert should not fire at the same time as kill")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("kill-switch did not cancel the run context")
	}
}

func TestBudget80PercentAlert(t *testing.T) {
	b := NewBudgetState(Budget{MaxUSD: 10}, 0)
	_, alert := b.Track(NewEvent(nil, RoleOrchestrator, EvDone, Done{Cost: &Cost{InputUSD: 8.5}}))
	if b.Spent() >= 10 {
		t.Fatal("8.5/10 must not kill")
	}
	if !alert {
		t.Fatal("8.5/10 is above 80% — alert expected")
	}
	// Only once.
	_, alert = b.Track(NewEvent(nil, RoleOrchestrator, EvDone, Done{Cost: &Cost{InputUSD: 0.1}}))
	if alert {
		t.Error("80% alert must fire once")
	}
}

func TestBudgetDailyAndWallClock(t *testing.T) {
	b := NewBudgetState(Budget{MaxDailyUSD: 5}, 5.0)
	if kill, _ := b.Track(NewEvent(nil, RoleOrchestrator, EvDone, Done{Cost: &Cost{}})); !kill {
		t.Error("daily cap reached before the run should kill")
	}

	b2 := NewBudgetState(Budget{MaxWallClock: 1 * time.Millisecond}, 0)
	time.Sleep(5 * time.Millisecond)
	if kill, _ := b2.Track(NewEvent(nil, RoleOrchestrator, EvDone, Done{Cost: &Cost{}})); !kill {
		t.Error("wall-clock cap should kill")
	}
}

func TestBudgetToolCaps(t *testing.T) {
	b := NewBudgetState(Budget{MaxToolCalls: 2}, 0)
	b.Track(NewEvent(nil, RoleOrchestrator, EvToolCall, ToolCall{Name: "read", Args: "{}"}))
	b.Track(NewEvent(nil, RoleOrchestrator, EvToolCall, ToolCall{Name: "read", Args: "{}"}))
	if kill, _ := b.Track(NewEvent(nil, RoleOrchestrator, EvToolCall, ToolCall{Name: "grep", Args: "{}"})); !kill {
		t.Error("max tool calls should kill")
	}

	b2 := NewBudgetState(Budget{MaxRepeated: 2}, 0)
	b2.Track(NewEvent(nil, RoleOrchestrator, EvToolCall, ToolCall{Name: "bash", Args: `{"c":"ls"}`}))
	b2.Track(NewEvent(nil, RoleOrchestrator, EvToolCall, ToolCall{Name: "bash", Args: `{"c":"ls"}`}))
	if kill, _ := b2.Track(NewEvent(nil, RoleOrchestrator, EvToolCall, ToolCall{Name: "bash", Args: `{"c":"ls"}`})); !kill {
		t.Error("max repeated tool calls should kill")
	}
}

func TestBudgetString(t *testing.T) {
	b := NewBudgetState(Budget{MaxUSD: 10}, 0)
	b.Track(NewEvent(nil, RoleOrchestrator, EvDone, Done{Cost: &Cost{InputUSD: 2}}))
	if s := b.String(); !strings.Contains(s, "2.00/10.00") {
		t.Errorf("String = %q", s)
	}
}

func TestAntiLoopReflectionInjection(t *testing.T) {
	repeat := ToolCall{ID: "c", Name: "bash", Args: `{"command":"ls"}`}
	p := &callProvider{calls: []ToolCall{repeat, repeat, repeat}, after: []string{"final"}, onlyFirst: true}
	loop := &Loop{
		Provider: p,
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
		AntiLoop: NewAntiLoop(8, 3),
	}
	var notes []string
	loop.OnEvent = func(ev StreamEvent) {
		if ev.Type == EvAdvisorNote {
			if n, ok := ev.Content.(AdvisorNote); ok {
				notes = append(notes, n.Note)
			}
		}
	}
	if err := loop.Run(context.Background(), "keep trying"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "anti-loop") {
		t.Fatalf("notes = %v", notes)
	}
	// The reflection prompt must be in history (visible to the human).
	found := false
	for _, msg := range loop.History {
		if msg.Role == "system" && strings.Contains(msg.Content, "Reflection") {
			found = true
		}
	}
	if !found {
		t.Errorf("reflection prompt missing: %+v", loop.History)
	}
}

func TestAntiLoopNoFalsePositive(t *testing.T) {
	a := NewAntiLoop(8, 3)
	different := []ToolCall{
		{Name: "read", Args: `{"p":"a"}`},
		{Name: "read", Args: `{"p":"b"}`},
		{Name: "read", Args: `{"p":"a"}`},
		{Name: "grep", Args: `{"p":"a"}`},
	}
	for _, c := range different {
		if a.Observe(c) {
			t.Fatalf("false positive on %+v", c)
		}
	}
}

func TestLoopBudgetIntegration(t *testing.T) {
	p := &ruleProvider{turns: []ruleTurn{{deltas: []string{"hello"}}}}
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := NewBudgetState(Budget{MaxUSD: 1}, 0)
	b.SetKill(cancel)
	loop := &Loop{
		Provider: p,
		Model:    "m",
		Tools:    map[string]Tool{},
		Gate:     GateFunc(AllowAll),
		Budget:   b,
	}
	if err := loop.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b.Spent() != 0 {
		t.Errorf("spent = %v", b.Spent())
	}
}
