package orchestrator

import (
	"context"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
)

func TestEstimateRunCostUsesCanonicalModelAndCompleteRequest(t *testing.T) {
	model := agentcore.CatalogModel{ID: "priced", Name: "Priced"}
	model.Cost.Input = 1
	model.Cost.Output = 2
	model.Limit.Context = 128_000
	model.Limit.Output = 1_000
	registry, err := agentcore.NewRegistry(context.Background(), &config.Config{}, mapKeyStore{"remote": "test-key"}, map[string]agentcore.CatalogProvider{
		"remote": {
			ID: "remote", Name: "Remote", API: "https://example.invalid/v1",
			Models: map[string]agentcore.CatalogModel{"priced": model},
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	provider, ok := registry.Provider("remote")
	if !ok {
		t.Fatal("remote provider missing")
	}
	orch := &Orchestrator{
		registry:   registry,
		guardrails: Guardrails{Budget: agentcore.NewBudgetState(agentcore.Budget{MaxUSD: 10}, 0)},
	}
	loop := &agentcore.Loop{
		Model: "priced", ContextWindow: 128_000, DefaultMaxTokens: 1_000,
		System:  []agentcore.Message{{Role: "system", Content: "system"}},
		History: []agentcore.Message{{Role: "user", Content: "hello"}},
		Budget:  orch.guardrails.Budget,
	}
	base := orch.estimateRunCost(provider, "remote/priced", loop, "")
	if base <= 0 {
		t.Fatalf("qualified-model estimate = %v, want priced canonical request", base)
	}
	withPrompt := orch.estimateRunCost(provider, "remote/priced", loop, strings.Repeat("prompt", 10_000))
	if withPrompt <= base {
		t.Fatalf("prompt estimate = %v, base %v; current task prompt was omitted", withPrompt, base)
	}
	loop.Tools = map[string]agentcore.Tool{
		"large": agentcore.NewToolFunc(agentcore.ToolSpec{
			Name: "large", Description: strings.Repeat("schema", 2_000),
			InputSchema: map[string]any{"type": "object"},
		}, nil),
	}
	withSchema := orch.estimateRunCost(provider, "remote/priced", loop, "")
	if withSchema <= base {
		t.Fatalf("complete request estimate = %v, base %v; tool schema was omitted", withSchema, base)
	}
}
