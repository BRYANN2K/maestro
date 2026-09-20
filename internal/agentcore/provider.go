// Package agentcore is the built-in LLM engine: the provider contract, the
// stream event vocabulary, the conversation loop, and the native sub-agent
// spawner. Every event crossing Maestro's layers is a StreamEvent — there is
// no parallel event vocabulary.
package agentcore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
)

const (
	// Providers must assemble tool calls before they can expose an EvToolCall to
	// Loop. Mirror Loop's default retained-output ceiling here so a hostile or
	// broken stream cannot hide unbounded argument fragments in provider-local
	// builders before the shared accounting layer sees them.
	providerToolPayloadLimit = loopDefaultMaxOutputBytes
	providerToolCallLimit    = 4096
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
	// Meta is optional. Leaving it nil avoids one heap allocation for every
	// streamed text/reasoning delta; callers that attach metadata can allocate
	// the map explicitly in their event literal.
	ev := StreamEvent{Type: typ, Role: role, Content: content}
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
	ProviderState json.RawMessage `json:",omitempty"`
	Usage         *Usage
	Cost          *Cost
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

func validateUsage(usage Usage) error {
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.CacheCreateTokens < 0 || usage.CacheHitTokens < 0 {
		return fmt.Errorf("provider returned negative token usage: %+v", usage)
	}
	return nil
}

func validateCost(cost Cost) error {
	values := []float64{cost.InputUSD, cost.OutputUSD, cost.CacheCreateUSD, cost.CacheHitUSD}
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("provider returned invalid cost: %+v", cost)
		}
	}
	if total := cost.Total(); math.IsNaN(total) || math.IsInf(total, 0) {
		return fmt.Errorf("provider returned invalid total cost: %+v", cost)
	}
	return nil
}

// Message is one conversational turn sent to a provider.
type Message struct {
	ProviderState json.RawMessage `json:",omitempty"`
	Role          string          // user | assistant | tool
	Content       string          // text payload
	Reasoning     string          // assistant-only: thinking-mode reasoning (must be
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

// sendStreamEvent applies cancellation-aware backpressure to provider
// streams. Loop may stop consuming as soon as a rule, output limit, or budget
// fires; selecting on ctx prevents the provider goroutine and response body
// from being stranded behind a full event channel.
func sendStreamEvent(ctx context.Context, ch chan<- StreamEvent, ev StreamEvent) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

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
