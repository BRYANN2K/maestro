package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
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
	// Harness selects the bundled OMP scheduler for production runs.
	Harness  bool
	Provider Provider
	Model    string
	// ContextWindow and DefaultMaxTokens are selected-model metadata used by
	// the request preflight before every provider turn.
	ContextWindow    int
	DefaultMaxTokens int
	Role             Role // orchestrator by default; set by Spawn
	Sampling         Sampling
	System           []Message
	Tools            map[string]Tool
	Gate             Gate
	History          []Message
	Stopper          *Stopper
	OnEvent          func(StreamEvent)
	MaxTurns         int // max provider turns per Run, 0 = unlimited (default 20)
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
		// A child timeout inherits an earlier parent deadline. Only attribute the
		// failure to Maestro's per-turn watchdog while the parent run is still
		// live; callers otherwise need the original cancellation/deadline so they
		// can avoid persisting it as a provider protocol failure.
		watchdogFired := errors.Is(turnCtx.Err(), context.DeadlineExceeded) && base.Err() == nil
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
	// Provider streams commonly split a response into hundreds or thousands
	// of small SSE deltas. Repeated `string += delta` copies the complete
	// response on every event (quadratic bytes and allocations). Builders keep
	// accumulation linear while still exposing the complete text to the
	// optional stream-rule matcher without another copy.
	var content strings.Builder
	var reasoning strings.Builder
	type thinkingAccumulator struct {
		block     ThinkingBlock
		thinking  strings.Builder
		signature strings.Builder
		data      strings.Builder
	}
	thinkingByIndex := map[int]int{}
	var thinking []*thinkingAccumulator
	specs := make([]ToolSpec, 0, len(l.Tools))
	for _, t := range l.Tools {
		specs = append(specs, t.Spec())
	}
	// Go map iteration is intentionally random. Stable tool ordering keeps
	// otherwise identical provider prefixes byte-for-byte cacheable and makes
	// request traces reproducible.
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	sampling := l.Sampling
	if sampling.MaxTokens <= 0 && l.DefaultMaxTokens > 0 {
		sampling.MaxTokens = l.DefaultMaxTokens
	} else if sampling.MaxTokens <= 0 && l.ContextWindow > 0 {
		sampling.MaxTokens = defaultReservedOutput
	}
	req := Request{
		Model:    l.Model,
		System:   l.System,
		Messages: l.History,
		Sampling: sampling,
		Tools:    specs,
	}
	plan, err := PlanContext(req, l.ContextWindow, l.DefaultMaxTokens)
	if err != nil {
		msg := "context preflight: " + err.Error()
		l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
		return Message{}, nil, nil, false, errors.New(msg)
	}
	if err := plan.Check(); err != nil {
		msg := "context preflight: " + err.Error()
		l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
		return Message{}, nil, nil, false, errors.New(msg)
	}
	var reservation *BudgetReservation
	if l.Budget != nil {
		// Re-run cost preflight for every provider turn. Tool results grow the
		// prompt between turns. This is the sole side-effecting admission point:
		// the outer orchestrator preview is deliberately read-only.
		cost, costErr := l.Provider.Cost(req, Usage{
			InputTokens: plan.EstimatedInputTokens, OutputTokens: plan.ReservedOutputTokens,
		})
		if costErr != nil {
			msg := "budget preflight: " + costErr.Error()
			l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
			return Message{}, nil, nil, false, errors.New(msg)
		}
		estimate := cost.Total()
		reservation, err = l.Budget.ReserveEstimate(ctx, estimate)
		if err != nil {
			msg := "budget preflight: " + err.Error()
			l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
			return Message{}, nil, nil, false, errors.New(msg)
		}
		// Once Stream succeeds the provider request may have incurred cost. Any
		// path without a valid Done retains the lease until its bounded expiry.
		defer func() { l.Budget.AbandonReservation(reservation) }()
	}
	ch, err := l.Provider.Stream(ctx, req)
	if err != nil {
		if l.Budget != nil {
			if releaseErr := l.Budget.ReleaseReservation(ctx, reservation); releaseErr != nil {
				reservation = nil
				return Message{}, nil, nil, false, releaseErr
			}
			reservation = nil
		}
		return Message{}, nil, nil, false, fmt.Errorf("stream: %w", err)
	}
	for ev := range ch {
		if l.Budget != nil {
			decision := l.Budget.TrackReservation(ctx, ev, reservation)
			if decision.Kill {
				budgetErr := l.Budget.Err()
				// A valid provider completion incurred real cost even when that
				// completion reaches the cap. Forward its accounting event before
				// the terminal budget error so RunResult/session totals stay exact.
				if ev.Type == EvDone && decision.AccountDone {
					l.emit(ev)
				}
				msg := "budget cap reached — run aborted"
				if budgetErr != nil {
					msg = "budget accounting failed — run aborted: " + budgetErr.Error()
				}
				l.emit(NewEvent(&l.seq, l.systemRole(), EvError, StreamError{Message: msg}))
				return Message{}, nil, nil, false, errors.New(msg)
			} else if decision.Alert {
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
				content.WriteString(td.Text)
				if l.Rules != nil && l.Rules.HasActive() {
					if reminder, fired := l.Rules.Check(content.String()); fired {
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
				reasoning.WriteString(rd.Text)
				if rd.Indexed || rd.BlockType != "" || rd.Signature != "" || rd.Data != "" {
					position, exists := thinkingByIndex[rd.Index]
					if !exists {
						position = len(thinking)
						thinkingByIndex[rd.Index] = position
						thinking = append(thinking, &thinkingAccumulator{block: ThinkingBlock{Type: rd.BlockType, Index: rd.Index}})
					}
					block := thinking[position]
					if rd.BlockType != "" {
						block.block.Type = rd.BlockType
					}
					block.thinking.WriteString(rd.Text)
					block.signature.WriteString(rd.Signature)
					block.data.WriteString(rd.Data)
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
		// Providers intentionally stop publishing when their request context is
		// canceled. Preserve that cause instead of converting a normal user cancel
		// or inherited deadline into a misleading truncated-stream error.
		if err := ctx.Err(); err != nil {
			return Message{}, nil, nil, false, err
		}
		return Message{}, nil, nil, false, errors.New("provider stream closed without a completion event")
	}
	assistant.ProviderState = done.ProviderState
	assistant.Content = content.String()
	assistant.Reasoning = reasoning.String()
	if len(thinking) > 0 {
		assistant.ThinkingBlocks = make([]ThinkingBlock, len(thinking))
		for i, accumulated := range thinking {
			block := accumulated.block
			block.Thinking = accumulated.thinking.String()
			block.Signature = accumulated.signature.String()
			block.Data = accumulated.data.String()
			assistant.ThinkingBlocks[i] = block
		}
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
	case Done:
		return len(value.ProviderState)
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
		call.Name, toolArgsPreview(call.Args))
	l.History = append(l.History, Message{Role: "system", Content: prompt})
	l.emit(NewEvent(&l.seq, l.systemRole(), EvAdvisorNote, AdvisorNote{Level: "concern", Note: "anti-loop: " + call.Name + " repeated — reflection injected"}))
}

const antiLoopArgsPreviewBytes = 512

func toolArgsPreview(args string) string {
	if len(args) <= antiLoopArgsPreviewBytes {
		return args
	}
	preview := args[:antiLoopArgsPreviewBytes]
	for preview != "" && !utf8.ValidString(preview) {
		preview = preview[:len(preview)-1]
	}
	return fmt.Sprintf("%s… [sha256:%s]", preview, sha256Sum(args)[:12])
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
