package agentcore

import (
	"encoding/json"
	"fmt"
)

const (
	// contextPlanFramingTokens covers provider-specific request framing that is
	// not represented by Request's JSON form. The portable estimate below
	// deliberately treats every encoded byte as a token; this is conservative
	// for the byte-backed tokenizers used by Maestro's native providers.
	contextPlanFramingTokens = 32
	defaultReservedOutput    = 4096
)

// ContextPlan is the deterministic preflight for one provider request.
// EstimatedInputTokens is a portable upper-bound estimate, not provider
// billing data. ReservedOutputTokens is the request max_tokens value or the
// selected model's default output limit.
type ContextPlan struct {
	ContextWindow        int
	EstimatedInputTokens int
	ReservedOutputTokens int
	TotalTokens          int
}

// PlanContext accounts for the complete normalized request before it leaves
// the process. Encoding Request includes system/history messages, content,
// reasoning and signed thinking blocks, tool calls/results, sampling and
// provider options, and every tool schema. encoding/json sorts string map keys,
// so equivalent maps produce the same estimate regardless of Go map order.
func PlanContext(req Request, contextWindow, defaultMaxTokens int) (ContextPlan, error) {
	encoded, err := json.Marshal(req)
	if err != nil {
		return ContextPlan{}, fmt.Errorf("estimate request: %w", err)
	}

	reserved := req.Sampling.MaxTokens
	if reserved <= 0 {
		reserved = defaultMaxTokens
	}
	if reserved <= 0 {
		reserved = defaultReservedOutput
	}
	input := len(encoded) + contextPlanFramingTokens
	if reserved > int(^uint(0)>>1)-input {
		return ContextPlan{}, fmt.Errorf("estimate request: token count overflows int")
	}
	return ContextPlan{
		ContextWindow:        contextWindow,
		EstimatedInputTokens: input,
		ReservedOutputTokens: reserved,
		TotalTokens:          input + reserved,
	}, nil
}

// Check rejects a request that cannot fit its configured model window. A
// zero/unknown context window leaves enforcement disabled; registry-backed
// native runs always transport known model metadata when it is available.
func (p ContextPlan) Check() error {
	if p.ContextWindow > 0 && p.TotalTokens > p.ContextWindow {
		return fmt.Errorf(
			"context window exceeded: estimated input %d tokens + reserved output %d tokens = %d, model window %d",
			p.EstimatedInputTokens, p.ReservedOutputTokens, p.TotalTokens, p.ContextWindow,
		)
	}
	return nil
}
