package agentcore

import (
	"strings"
	"testing"
)

func TestPlanContextAccountsForCompleteRequestDeterministically(t *testing.T) {
	temperature := 0.2
	req := Request{
		Model:  "provider/model",
		System: []Message{{Role: "system", Content: strings.Repeat("s", 300)}},
		Messages: []Message{
			{
				Role:      "assistant",
				Content:   strings.Repeat("c", 300),
				Reasoning: strings.Repeat("r", 300),
				ThinkingBlocks: []ThinkingBlock{{
					Type: "thinking", Thinking: strings.Repeat("t", 300),
					Signature: strings.Repeat("g", 300), Data: strings.Repeat("d", 300), Index: 2,
				}},
				ToolCalls: []ToolCall{{ID: "call", Name: "lookup", Args: strings.Repeat("a", 300)}},
			},
			{Role: "tool", ToolCallID: "call", Name: "lookup", Content: strings.Repeat("o", 300)},
		},
		Sampling: Sampling{
			Temperature: &temperature,
			ProviderOptions: map[string]any{
				"zeta":  strings.Repeat("p", 300),
				"alpha": map[string]any{"enabled": true},
			},
		},
		Tools: []ToolSpec{{
			Name: "lookup", Description: strings.Repeat("x", 300),
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"description": strings.Repeat("q", 300), "type": "string"}},
			},
		}},
	}
	first, err := PlanContext(req, 32_000, 2_048)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanContext(req, 32_000, 2_048)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("non-deterministic plans: first=%+v second=%+v", first, second)
	}
	if first.ReservedOutputTokens != 2_048 {
		t.Fatalf("reserved output = %d, want model default 2048", first.ReservedOutputTokens)
	}
	// Each 300-byte payload above must be represented. This lower bound makes
	// regressions that omit a request surface visible without binding the test
	// to encoding/json's exact struct-field envelope.
	if first.EstimatedInputTokens < 3_000 {
		t.Fatalf("input estimate = %d, complete request was not accounted for", first.EstimatedInputTokens)
	}
}

func TestPlanContextSamplingMaxTokensOverridesModelDefault(t *testing.T) {
	plan, err := PlanContext(Request{Sampling: Sampling{MaxTokens: 777}}, 32_000, 2_048)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ReservedOutputTokens != 777 {
		t.Fatalf("reserved output = %d, want request max 777", plan.ReservedOutputTokens)
	}
}

func TestPlanContextRejectsUnencodableProviderOptions(t *testing.T) {
	_, err := PlanContext(Request{Sampling: Sampling{ProviderOptions: map[string]any{"bad": func() {}}}}, 32_000, 2_048)
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("PlanContext error = %v, want unsupported provider option", err)
	}
}
