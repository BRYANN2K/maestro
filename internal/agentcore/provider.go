// Package agentcore is the built-in LLM engine: the provider contract, the
// stream event vocabulary, the conversation loop, and the native sub-agent
// spawner. Every event crossing Maestro's layers is a StreamEvent — there is
// no parallel event vocabulary.
package agentcore

import (
	"context"
	"errors"
	"fmt"
)

// Role identifies which agent produced an event.
type Role string

// Agent roles.
const (
	RoleOrchestrator Role = "orchestrator"
	RoleDev          Role = "dev"
	RoleReviewer     Role = "reviewer"
	RoleDocs         Role = "docs"
	RoleAdvisor      Role = "advisor"
)

// EventType enumerates the stream event kinds.
type EventType string

// Stream event types.
const (
	EvTextDelta      EventType = "text_delta"
	EvReasoningDelta EventType = "reasoning_delta"
	EvToolCall       EventType = "tool_call"
	EvToolResult     EventType = "tool_result"
	EvPhaseChange    EventType = "phase_change"
	EvSubAgent       EventType = "sub_agent"
	EvAsk            EventType = "ask"
	EvHITL           EventType = "hitl"
	EvAdvisorNote    EventType = "advisor_note"
	EvError          EventType = "error"
	EvDone           EventType = "done"
)

// StreamEvent is the single event type crossing layers: conversation turns,
// sub-agent tool calls, native spawns, legacy subprocess streams, ask
// prompts, HITL choices. Content holds one of the payload structs below.
type StreamEvent struct {
	Type    EventType
	Role    Role
	Seq     uint64
	Content any
	Meta    map[string]string
}

// NewEvent builds an event with the next sequence number.
func NewEvent(seq *uint64, role Role, typ EventType, content any) StreamEvent {
	ev := StreamEvent{Type: typ, Role: role, Content: content, Meta: map[string]string{}}
	if seq != nil {
		*seq++
		ev.Seq = *seq
	}
	return ev
}

// Payload structs — the closed set of StreamEvent.Content values.

// TextDelta is an incremental chunk of assistant text.
type TextDelta struct {
	Text    string
	Index   int // Anthropic content order
	Indexed bool
}

// ToolCall is an assistant-initiated tool invocation.
type ToolCall struct {
	ID                  string
	Name                string
	Args                string // JSON object
	ContentBlockIndex   int    // Anthropic content order; ignored elsewhere
	ContentBlockIndexed bool
}

// ToolResult is the terminal outcome of one tool execution. Output may be
// empty for a successful tool; EvToolCall is the separate running marker.
type ToolResult struct {
	ID     string
	Name   string
	Output string
	Err    string // empty on success
}

// PhaseChange reports a pipeline phase transition.
type PhaseChange struct {
	From string
	To   string
}

// SubAgentStatus reports a sub-agent's lifecycle state.
type SubAgentStatus struct {
	Role   string // dev | reviewer | docs
	Status string // running | done | error | cancelled
	Detail string
}

// Ask is a structured question for the human, rendered as an option picker.
type Ask struct {
	ID          string
	Question    string
	Options     []string
	Recommended int // index into Options, -1 for none
}

// HITLItem is one human action required to complete the current phase.
type HITLItem struct {
	ID       string
	Item     string
	Status   string // pending | done
	Blocking bool   // true only when the orchestrator enforces this action as a gate
}

// AdvisorNote is a typed note from the advisor model.
type AdvisorNote struct {
	Level string // info | concern | blocker
	Note  string
}

// StreamError carries a terminal stream failure.
type StreamError struct {
	Message string
}

// Error implements error for StreamError.
func (e StreamError) Error() string { return e.Message }

// Done marks the end of a turn with optional usage accounting.
type Done struct {
	Usage *Usage
	Cost  *Cost
}

// Usage is token accounting for one turn.
type Usage struct {
	InputTokens       int
	OutputTokens      int
	CacheCreateTokens int
	CacheHitTokens    int
}

// Cost is the estimated USD cost of a turn, computed from model pricing.
type Cost struct {
	InputUSD       float64
	OutputUSD      float64
	CacheCreateUSD float64
	CacheHitUSD    float64
}

// Total returns the summed cost.
func (c Cost) Total() float64 {
	return c.InputUSD + c.OutputUSD + c.CacheCreateUSD + c.CacheHitUSD
}

// Message is one conversational turn sent to a provider.
type Message struct {
	Role      string // user | assistant | tool
	Content   string // text payload
	Reasoning string // assistant-only: thinking-mode reasoning (must be
	// passed back to reasoning providers on the next request)
	ThinkingBlocks   []ThinkingBlock // signed Anthropic blocks, echoed verbatim
	TextBlockIndex   int             // Anthropic content order
	TextBlockIndexed bool
	ToolCalls        []ToolCall // assistant role only
	ToolCallID       string     // tool role only
	Name             string     // tool role only
}

// ThinkingBlock is a complete Anthropic thinking/redacted_thinking block.
// Signed blocks must be passed back unchanged during a tool-use turn.
type ThinkingBlock struct {
	Type      string // thinking | redacted_thinking
	Thinking  string
	Signature string
	Data      string
	Index     int
}

// ReasoningDelta is one chunk of thinking-mode reasoning content.
type ReasoningDelta struct {
	Text      string
	Signature string
	Data      string
	BlockType string // thinking | redacted_thinking
	Index     int
	Indexed   bool
}

// ToolSpec is the schema a tool exposes to the model.
type ToolSpec struct {
	Name          string
	Description   string
	InputSchema   map[string]any
	NeedsApproval bool
}

// Request is the normalized input every provider accepts.
type Request struct {
	Model    string
	System   []Message
	Messages []Message
	Sampling Sampling
	Tools    []ToolSpec
}

// Provider is the engine backend contract. Stream runs one turn and returns
// a channel of events closed after EvDone or EvError. Cost estimates the
// price of a completed turn from the provider's model pricing.
type Provider interface {
	Name() string
	Type() string
	Models() []Model
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
	Cost(req Request, usage Usage) (Cost, error)
}

// AskFunc answers a structured human question: it returns the index of the
// chosen option, or an error when the human cancels. The interactive TUI
// provides it; headless runs leave it nil so the ask tool reports a clear
// error instead of failing silently.
type AskFunc func(ctx context.Context, question string, options []string, recommended int) (int, error)

// ErrProviderClosed is returned by Stream when the provider is shut down.
var ErrProviderClosed = errors.New("provider closed")

// CostOf computes the cost for usage against a model's pricing.
func CostOf(m Model, usage Usage) Cost {
	return Cost{
		InputUSD:       float64(usage.InputTokens) / 1e6 * m.PriceInput,
		OutputUSD:      float64(usage.OutputTokens) / 1e6 * m.PriceOutput,
		CacheCreateUSD: float64(usage.CacheCreateTokens) / 1e6 * m.PriceCacheCreate,
		CacheHitUSD:    float64(usage.CacheHitTokens) / 1e6 * m.PriceCacheHit,
	}
}

// ErrContent reports an unexpected payload type for an event.
func ErrContent(typ EventType, got any) error {
	return fmt.Errorf("event %s: unexpected content %T", typ, got)
}
