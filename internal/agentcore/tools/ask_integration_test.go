package tools

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

type scriptTurn struct {
	deltas []string
	calls  []agentcore.ToolCall
}

type scriptProvider struct {
	mu    sync.Mutex
	turns []scriptTurn
	call  int
}

func (f *scriptProvider) Name() string { return "script" }
func (f *scriptProvider) Type() string { return "script" }
func (f *scriptProvider) Models() []agentcore.Model {
	return nil
}
func (f *scriptProvider) Cost(req agentcore.Request, usage agentcore.Usage) (agentcore.Cost, error) {
	return agentcore.Cost{}, nil
}

func (f *scriptProvider) Stream(ctx context.Context, req agentcore.Request) (<-chan agentcore.StreamEvent, error) {
	f.mu.Lock()
	idx := f.call
	f.call++
	turn := f.turns[idx]
	f.mu.Unlock()
	ch := make(chan agentcore.StreamEvent, 16)
	go func() {
		defer close(ch)
		for _, d := range turn.deltas {
			ch <- agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: d})
		}
		for _, c := range turn.calls {
			ch <- agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvToolCall, c)
		}
		ch <- agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})
	}()
	return ch, nil
}

// TestAskToolEndToEnd drives a real loop turn where the model calls the ask
// tool; the test answers the question through the queue and asserts the
// answer lands back in the conversation (the wiring that used to be a stub).
func TestAskToolEndToEnd(t *testing.T) {
	askQ := NewAskQueue(4)

	provider := &scriptProvider{turns: []scriptTurn{
		{
			deltas: []string{"I need to know which engine to use."},
			calls: []agentcore.ToolCall{{
				ID: "ask_1", Name: "ask",
				Args: `{"question":"Which engine?","options":["native","legacy: codex"],"recommended":0}`,
			}},
		},
		{
			deltas: []string{"Got it — using your choice."},
		},
	}}

	reg := New()
	reg.Add(NewRead())
	reg.Add(NewAsk(askQ.Ask))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan agentcore.AgentResult, 1)
	go func() {
		loop, err := agentcore.Spawn(ctx, agentcore.SpawnOptions{
			Role:     agentcore.RoleOrchestrator,
			Provider: provider,
			Model:    "fake-model",
			Tools:    reg.Map(),
			Gate:     agentcore.GateFunc(agentcore.AllowAll),
		})
		if err != nil {
			done <- agentcore.AgentResult{OK: false, Summary: "err: " + err.Error()}
			return
		}
		res, rerr := agentcore.RunResult(ctx, loop, "test task")
		if rerr != nil {
			done <- agentcore.AgentResult{OK: false, Summary: "err: " + rerr.Error()}
			return
		}
		done <- res
	}()

	// The loop is now blocked on the ask tool: a question is queued.
	var req *AskRequest
	deadline := time.Now().Add(5 * time.Second)
	for req == nil && time.Now().Before(deadline) {
		req = askQ.Next()
		time.Sleep(10 * time.Millisecond)
	}
	if req == nil {
		t.Fatal("ask tool did not queue a question — the tool is not wired")
	}
	if req.Question != "Which engine?" || len(req.Options) != 2 || req.Options[1] != "legacy: codex" {
		t.Fatalf("queued question = %+v", req)
	}
	t.Logf("→ question reçue : %q (options %v, recommended %d)", req.Question, req.Options, req.Recommended)

	// Answer: option 2 ("legacy: codex").
	askQ.Answer(req, 1)

	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("turn failed: %s", res.Summary)
		}
		if !strings.Contains(res.Summary, "your choice") {
			t.Errorf("summary = %q, want the model acknowledging the answer", res.Summary)
		}
		// The second scripted turn ran, proving the answer was delivered to
		// the conversation and the loop resumed.
		t.Logf("→ tour terminé : %q", res.Summary)
	case <-ctx.Done():
		t.Fatal("turn did not complete after answering")
	}
}

// TestAskToolCancelEndToEnd drives the same flow but the user cancels (esc):
// the tool reports a clean cancellation error and the loop keeps running
// instead of hanging or crashing.
func TestAskToolCancelEndToEnd(t *testing.T) {
	askQ := NewAskQueue(4)

	provider := &scriptProvider{turns: []scriptTurn{
		{
			deltas: []string{"Please choose."},
			calls: []agentcore.ToolCall{{
				ID: "ask_1", Name: "ask",
				Args: `{"question":"Which?","options":["a","b"]}`,
			}},
		},
		{
			deltas: []string{"Understood — skipping."},
		},
	}}

	reg := New()
	reg.Add(NewAsk(askQ.Ask))

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan agentcore.AgentResult, 1)
	go func() {
		loop, err := agentcore.Spawn(ctx, agentcore.SpawnOptions{
			Role:     agentcore.RoleOrchestrator,
			Provider: provider,
			Model:    "fake-model",
			Tools:    reg.Map(),
			Gate:     agentcore.GateFunc(agentcore.AllowAll),
		})
		if err != nil {
			done <- agentcore.AgentResult{OK: false, Summary: "err: " + err.Error()}
			return
		}
		res, rerr := agentcore.RunResult(ctx, loop, "test task")
		if rerr != nil {
			done <- agentcore.AgentResult{OK: false, Summary: "err: " + rerr.Error()}
			return
		}
		done <- res
	}()

	var req *AskRequest
	deadline := time.Now().Add(5 * time.Second)
	for req == nil && time.Now().Before(deadline) {
		req = askQ.Next()
		time.Sleep(10 * time.Millisecond)
	}
	if req == nil {
		t.Fatal("ask tool did not queue a question")
	}
	askQ.Answer(req, -1) // esc → cancel

	select {
	case res := <-done:
		if !res.OK {
			t.Fatalf("turn failed after cancel: %s", res.Summary)
		}
		t.Logf("→ tour continué après annulation : %q", res.Summary)
	case <-ctx.Done():
		t.Fatal("turn did not complete after cancel")
	}
}
