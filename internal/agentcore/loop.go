package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Gate authorizes tool execution. The loop asks before every tool call.
type Gate interface {
	Authorize(ctx context.Context, call ToolCall, spec ToolSpec) error
}

// GateFunc adapts a function to Gate.
type GateFunc func(ctx context.Context, call ToolCall, spec ToolSpec) error

// Authorize calls the wrapped function.
func (f GateFunc) Authorize(ctx context.Context, call ToolCall, spec ToolSpec) error {
	return f(ctx, call, spec)
}

// AllowAll is a Gate that approves everything (tests, --yolo).
func AllowAll(ctx context.Context, call ToolCall, spec ToolSpec) error { return nil }

// Tool executes one model-invoked function.
type Tool interface {
	Spec() ToolSpec
	Run(ctx context.Context, args map[string]any) (string, error)
}

// ToolFunc adapts a function to Tool.
type ToolFunc struct {
	spec ToolSpec
	fn   func(ctx context.Context, args map[string]any) (string, error)
}

// NewToolFunc builds a Tool from a spec and a run function.
func NewToolFunc(spec ToolSpec, fn func(ctx context.Context, args map[string]any) (string, error)) Tool {
	return &ToolFunc{spec: spec, fn: fn}
}

// Spec returns the tool's schema.
func (t *ToolFunc) Spec() ToolSpec { return t.spec }

// Run calls the wrapped function.
func (t *ToolFunc) Run(ctx context.Context, args map[string]any) (string, error) {
	return t.fn(ctx, args)
}

// Stopper implements the two-press cancel contract: the first press cancels
// the active tour, the second quits.
type Stopper struct {
	mu         sync.Mutex
	tourCancel context.CancelFunc
	quitCh     chan struct{}
	quits      bool
}

// NewStopper returns a Stopper with no active tour.
func NewStopper() *Stopper {
	return &Stopper{quitCh: make(chan struct{})}
}

// Reset binds (or unbinds, with nil) the cancel func of the active tour.
func (s *Stopper) Reset(cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tourCancel = cancel
}

// Press cancels the active tour, or quits when no tour is running.
func (s *Stopper) Press() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tourCancel != nil {
		s.tourCancel()
		s.tourCancel = nil
		return
	}
	if !s.quits {
		s.quits = true
		close(s.quitCh)
	}
}

// Quit returns a channel closed when the stopper quits.
func (s *Stopper) Quit() <-chan struct{} { return s.quitCh }

// Loop is the conversation loop. The same loop drives the orchestrator's
// chat and, spawned with scoped tools, the native sub-agents.
type Loop struct {
	Provider Provider
	Model    string
	Role     Role // orchestrator by default; set by Spawn
	Sampling Sampling
	System   []Message
	Tools    map[string]Tool
	Gate     Gate
	History  []Message
	Stopper  *Stopper
	OnEvent  func(StreamEvent)
	MaxTurns int // max provider turns per Run, 0 = unlimited (default 20)
	// MaxOutputBytes bounds cumulative provider deltas and tool results retained
	// during one Run. Zero selects the safe default; it cannot be disabled.
	MaxOutputBytes int
	Rules          *RuleSet     // F1: dormant stream rules
	Budget         *BudgetState // F2: budget guardrails
	AntiLoop       *AntiLoop    // F3: repeat detection

	MaxTurn time.Duration // max wall time per provider turn, 0 = default

	seq         uint64
	outputBytes int
}

// systemRole returns the loop's role, defaulting to orchestrator.
func (l *Loop) systemRole() Role {
	if l.Role == "" {
		return RoleOrchestrator
	}
	return l.Role
}

// LastAssistantText returns the text of the final assistant message.
func (l *Loop) LastAssistantText() string {
	for i := len(l.History) - 1; i >= 0; i-- {
		if l.History[i].Role == "assistant" {
			return l.History[i].Content
		}
	}
	return ""
}

const loopDefaultMaxTurns = 20

const loopDefaultMaxOutputBytes = 8 << 20

// loopDefaultMaxTurn bounds one provider turn: a silent provider must not
// hold the run forever (the SSE idle watchdog fires earlier for quiet
// streams; this is the last-resort ceiling).
const loopDefaultMaxTurn = 15 * time.Minute

// Run executes one user turn: stream a response, execute any tool calls
// (each through the gate), and continue until the model stops calling tools.
func (l *Loop) Run(ctx context.Context, userPrompt string) error {
	l.outputBytes = 0
	if l.Gate == nil {
		l.Gate = GateFunc(AllowAll)
	}
	if l.Stopper == nil {
		l.Stopper = NewStopper()
	}
	maxTurns := l.MaxTurns
	if maxTurns <= 0 {
		maxTurns = loopDefaultMaxTurns
	}
	maxTurn := l.MaxTurn
	if maxTurn <= 0 {
		maxTurn = loopDefaultMaxTurn
	}
	base, cancel := context.WithCancel(ctx)
	defer cancel()
	l.History = append(l.History, Message{Role: "user", Content: userPrompt})
	for turn := 0; turn < maxTurns; turn++ {
		// Turn watchdog: each provider turn must complete within maxTurn,
		// otherwise the context expires and the stream aborts.
		turnCtx, tourCancel := context.WithTimeout(base, maxTurn)
		l.Stopper.Reset(tourCancel)
		assistant, calls, _, injected, err := l.oneTurn(turnCtx)
		watchdogFired := turnCtx.Err() == context.DeadlineExceeded
		if err != nil {
			tourCancel()
			l.Stopper.Reset(nil)
			if watchdogFired {
				msg := fmt.Sprintf("turn watchdog: provider produced no output within %s", maxTurn)
				l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
				return fmt.Errorf("%s", msg)
			}
			return err
		}
		if injected {
			tourCancel()
			l.Stopper.Reset(nil)
			// F1: a stream rule fired mid-token. The reminder was appended
			// to History as a system message (survives compaction) and the
			// turn restarts at the same point with it injected.
			continue
		}
		if assistant.Content != "" || assistant.Reasoning != "" || len(assistant.ThinkingBlocks) > 0 || len(assistant.ToolCalls) > 0 {
			l.History = append(l.History, assistant)
		}
		if len(calls) == 0 {
			tourCancel()
			l.Stopper.Reset(nil)
			return nil
		}
		for _, c := range calls {
			if l.AntiLoop != nil && l.AntiLoop.Observe(c) {
				l.injectAntiLoop(c)
			}
			result, err := l.runTool(turnCtx, c)
			if err != nil {
				tourCancel()
				l.Stopper.Reset(nil)
				return err
			}
			l.History = append(l.History, result)
			if turnCtx.Err() != nil {
				turnErr := turnCtx.Err()
				tourCancel()
				l.Stopper.Reset(nil)
				if turnErr == context.DeadlineExceeded {
					msg := fmt.Sprintf("turn watchdog: provider/tool turn exceeded %s", maxTurn)
					l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
					return errors.New(msg)
				}
				if base.Err() != nil {
					return base.Err()
				}
				return context.Canceled
			}
		}
		tourCancel()
		l.Stopper.Reset(nil)
		select {
		case <-l.Stopper.Quit():
			return context.Canceled
		default:
		}
	}
	return fmt.Errorf("loop exceeded %d turns", maxTurns)
}

// oneTurn streams one provider turn, collecting the assistant message and
// tool calls. injected reports a mid-token stream-rule interruption (F1):
// the partial output is dropped, a reminder was appended to History, and
// the caller must re-run the turn.
func (l *Loop) oneTurn(ctx context.Context) (assistant Message, calls []ToolCall, done *Done, injected bool, err error) {
	thinkingByIndex := map[int]int{}
	specs := make([]ToolSpec, 0, len(l.Tools))
	for _, t := range l.Tools {
		specs = append(specs, t.Spec())
	}
	req := Request{
		Model:    l.Model,
		System:   l.System,
		Messages: l.History,
		Sampling: l.Sampling,
		Tools:    specs,
	}
	if l.Budget != nil {
		if est := l.Budget.Estimate(req); est > 0 {
			l.emit(NewEvent(&l.seq, l.systemRole(), EvHITL, HITLItem{ID: "budget-estimate", Item: fmt.Sprintf("estimated cost $%.4f", est), Status: "done"}))
		}
	}
	ch, err := l.Provider.Stream(ctx, req)
	if err != nil {
		return Message{}, nil, nil, false, fmt.Errorf("stream: %w", err)
	}
	for ev := range ch {
		if l.Budget != nil {
			if kill, alert := l.Budget.Track(ev); kill {
				l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: "budget cap reached — run aborted"}))
				return Message{}, nil, nil, false, fmt.Errorf("budget cap reached")
			} else if alert {
				l.emit(NewEvent(&l.seq, l.systemRole(), EvHITL, HITLItem{ID: "budget-alert", Item: "budget at 80%", Status: "pending"}))
			}
		}
		if err := l.reserveOutput(streamEventOutputBytes(ev)); err != nil {
			return Message{}, nil, nil, false, err
		}
		l.emit(ev)
		switch ev.Type {
		case EvTextDelta:
			if td, ok := ev.Content.(TextDelta); ok {
				if td.Indexed && !assistant.TextBlockIndexed {
					assistant.TextBlockIndex = td.Index
					assistant.TextBlockIndexed = true
				}
				assistant.Content += td.Text
				if l.Rules != nil {
					if reminder, fired := l.Rules.Check(assistant.Content); fired {
						// Mid-token interruption: drop the partial output,
						// inject the rule as a system reminder, resume.
						l.emit(NewEvent(&l.seq, l.systemRole(), EvAdvisorNote, AdvisorNote{Level: "concern", Note: "stream rule fired: " + reminder}))
						l.History = append(l.History, Message{Role: "system", Content: reminder})
						return Message{}, nil, nil, true, nil
					}
				}
			}
		case EvReasoningDelta:
			if rd, ok := ev.Content.(ReasoningDelta); ok {
				// Thinking-mode reasoning must be kept verbatim: reasoning
				// providers reject the next request without it.
				assistant.Reasoning += rd.Text
				if rd.Indexed || rd.BlockType != "" || rd.Signature != "" || rd.Data != "" {
					position, exists := thinkingByIndex[rd.Index]
					if !exists {
						position = len(assistant.ThinkingBlocks)
						thinkingByIndex[rd.Index] = position
						assistant.ThinkingBlocks = append(assistant.ThinkingBlocks, ThinkingBlock{Type: rd.BlockType, Index: rd.Index})
					}
					block := &assistant.ThinkingBlocks[position]
					if rd.BlockType != "" {
						block.Type = rd.BlockType
					}
					block.Thinking += rd.Text
					block.Signature += rd.Signature
					block.Data += rd.Data
				}
			}
		case EvToolCall:
			if tc, ok := ev.Content.(ToolCall); ok {
				assistant.ToolCalls = append(assistant.ToolCalls, tc)
				calls = append(calls, tc)
			}
		case EvDone:
			if d, ok := ev.Content.(Done); ok {
				d := d
				done = &d
			}
		case EvError:
			if se, ok := ev.Content.(StreamError); ok {
				return Message{}, nil, nil, false, errors.New(se.Message)
			}
		}
	}
	if done == nil {
		return Message{}, nil, nil, false, errors.New("provider stream closed without a completion event")
	}
	assistant.Role = "assistant"
	return assistant, calls, done, false, nil
}

func streamEventOutputBytes(ev StreamEvent) int {
	switch value := ev.Content.(type) {
	case TextDelta:
		return len(value.Text)
	case ReasoningDelta:
		return len(value.Text) + len(value.Signature) + len(value.Data)
	case ToolCall:
		return len(value.ID) + len(value.Name) + len(value.Args)
	case StreamError:
		return len(value.Message)
	case *StreamError:
		if value != nil {
			return len(value.Message)
		}
	}
	return 0
}

func (l *Loop) reserveOutput(size int) error {
	if size <= 0 {
		return nil
	}
	limit := l.MaxOutputBytes
	if limit <= 0 {
		limit = loopDefaultMaxOutputBytes
	}
	if size > limit-l.outputBytes {
		msg := fmt.Sprintf("agent output exceeded %d bytes", limit)
		l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
		return errors.New(msg)
	}
	l.outputBytes += size
	return nil
}

// injectAntiLoop appends the guided reflection prompt (F3) and announces it.
func (l *Loop) injectAntiLoop(call ToolCall) {
	prompt := fmt.Sprintf(
		"Reflection: you have called %s(%s) repeatedly with the same arguments. "+
			"Step back: is this still making progress? If not, change approach or explain why "+
			"the repetition is necessary. Do not repeat the same call unchanged.",
		call.Name, call.Args)
	l.History = append(l.History, Message{Role: "system", Content: prompt})
	l.emit(NewEvent(&l.seq, l.systemRole(), EvAdvisorNote, AdvisorNote{Level: "concern", Note: "anti-loop: " + call.Name + " repeated — reflection injected"}))
}

// runTool gates and executes one tool call, returning the tool-result
// message for the history.
func (l *Loop) runTool(ctx context.Context, call ToolCall) (Message, error) {
	tool, ok := l.Tools[call.Name]
	if !ok {
		msg := fmt.Sprintf("unknown tool %q", call.Name)
		if err := l.reserveOutput(len(msg)); err != nil {
			return Message{}, err
		}
		l.emit(NewEvent(&l.seq, RoleOrchestrator, EvToolResult, ToolResult{ID: call.ID, Name: call.Name, Err: msg}))
		return Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: "error: " + msg}, nil
	}
	if err := l.Gate.Authorize(ctx, call, tool.Spec()); err != nil {
		msg := fmt.Sprintf("denied by gate: %v", err)
		if limitErr := l.reserveOutput(len(msg)); limitErr != nil {
			return Message{}, limitErr
		}
		l.emit(NewEvent(&l.seq, RoleOrchestrator, EvToolResult, ToolResult{ID: call.ID, Name: call.Name, Err: msg}))
		return Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: "error: " + msg}, nil
	}
	var args map[string]any
	if err := unmarshalArgs(call.Args, &args); err != nil {
		msg := fmt.Sprintf("invalid tool args: %v", err)
		if limitErr := l.reserveOutput(len(msg)); limitErr != nil {
			return Message{}, limitErr
		}
		l.emit(NewEvent(&l.seq, RoleOrchestrator, EvToolResult, ToolResult{ID: call.ID, Name: call.Name, Err: msg}))
		return Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: "error: " + msg}, nil
	}
	out, err := tool.Run(ctx, args)
	if err != nil {
		msg := err.Error()
		if limitErr := l.reserveOutput(len(msg)); limitErr != nil {
			return Message{}, limitErr
		}
		l.emit(NewEvent(&l.seq, RoleOrchestrator, EvToolResult, ToolResult{ID: call.ID, Name: call.Name, Err: msg}))
		return Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: "error: " + msg}, nil
	}
	if err := l.reserveOutput(len(out)); err != nil {
		return Message{}, err
	}
	l.emit(NewEvent(&l.seq, RoleOrchestrator, EvToolResult, ToolResult{ID: call.ID, Name: call.Name, Output: out}))
	return Message{Role: "tool", ToolCallID: call.ID, Name: call.Name, Content: out}, nil
}

func (l *Loop) emit(ev StreamEvent) {
	if ev.Role == "" {
		ev.Role = RoleOrchestrator
	}
	// Providers have their own per-request counters. A loop can perform
	// several provider turns, so forwarding those counters produces duplicate
	// sequence numbers. The loop is the canonical ordering boundary.
	l.seq++
	ev.Seq = l.seq
	if l.OnEvent != nil {
		l.OnEvent(ev)
	}
}

func unmarshalArgs(raw string, dst *map[string]any) error {
	if raw == "" {
		*dst = map[string]any{}
		return nil
	}
	return json.Unmarshal([]byte(raw), dst)
}
