package orchestrator

import (
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/session"
)

func TestRulesCompiledOnAccept(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := t.Context()

	if _, err := orch.Propose(ctx, "Add auth with panic guards"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	// Inject a Stream Rules block into the draft, then accept.
	orch.sess.Draft.Body += "\n## Stream Rules\n- forbid: `panic\\(`\n  because: Never panic.\n"
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if orch.guardrails.Rules == nil {
		t.Fatal("rules not compiled after accept")
	}
	if orch.RuleCount() != 0 {
		t.Errorf("rules should be dormant, fired = %d", orch.RuleCount())
	}
	// The watcher fires on violation.
	if reminder, ok := orch.guardrails.Rules.Check("we panic( here"); !ok || !strings.Contains(reminder, "panic") {
		t.Errorf("check = %q, %v", reminder, ok)
	}
	if orch.RuleCount() != 1 {
		t.Errorf("fired = %d", orch.RuleCount())
	}
}

func TestRulesRecompiledOnEdit(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := t.Context()

	// Edit appends refinements to the draft; the ruleset recompiles with
	// the edited body.
	if _, err := orch.Propose(ctx, "Add auth"); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if err := orch.Edit(ctx, "add rule: no _ = err"); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	orch.sess.Draft.Body += "\n## Stream Rules\n- forbid: `_ = err`\n"
	if err := orch.Edit(ctx, "final tweak"); err != nil {
		t.Fatalf("Edit 2: %v", err)
	}
	if _, err := orch.Accept(ctx, BranchChoice{Kind: "stay"}); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if orch.guardrails.Rules == nil {
		t.Fatal("rules not compiled after accept")
	}
	if _, ok := orch.guardrails.Rules.Check("_ = err"); !ok {
		t.Error("rule from the edited body should be active")
	}
	if orch.Phase() != session.PhaseSpec {
		t.Errorf("phase = %q", orch.Phase())
	}
}

func TestBudgetFromConfig(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{
		Options: map[string]string{
			"budget-max-usd":        "1.5",
			"budget-max-tool-calls": "20",
		},
	}
	var out strings.Builder
	orch, err := New(t.Context(), Options{
		ProjectDir:  dir,
		SessionsDir: t.TempDir() + "/s",
		Config:      cfg,
		In:          strings.NewReader(""),
		Out:         &out,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if orch.BudgetState() == nil {
		t.Fatal("budget not built from config")
	}
	if s := orch.BudgetState().String(); !strings.Contains(s, "0.00/1.50") {
		t.Errorf("budget = %q", s)
	}
}

func TestBudgetDisabledWithoutConfig(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if orch.BudgetState() != nil {
		t.Error("budget should be disabled without config options")
	}
}

func TestBudgetKillBindsToRun(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{Options: map[string]string{"budget-max-usd": "0.0001"}}
	var out strings.Builder
	orch, err := New(t.Context(), Options{
		ProjectDir:  dir,
		SessionsDir: t.TempDir() + "/s",
		Config:      cfg,
		In:          strings.NewReader(""),
		Out:         &out,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := orch.bindBudgetKill(t.Context())
	defer cancel()
	orch.guardrails.Budget.Kill()
	if ctx.Err() == nil {
		t.Error("kill-switch did not cancel the run context")
	}
}

func TestBudgetCountersResetForEachRun(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{Options: map[string]string{"budget-max-usd": "1"}}
	orch, err := New(t.Context(), Options{
		ProjectDir:  dir,
		SessionsDir: t.TempDir() + "/s",
		Config:      cfg,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx1, cancel1 := orch.bindBudgetKill(t.Context())
	orch.guardrails.Budget.Track(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvDone, agentcore.Done{Cost: &agentcore.Cost{InputUSD: 0.25}}))
	cancel1()
	if ctx1.Err() == nil {
		t.Fatal("first run context was not cancelled")
	}
	_, cancel2 := orch.bindBudgetKill(t.Context())
	defer cancel2()
	if got := orch.guardrails.Budget.Spent(); got != 0 {
		t.Fatalf("second run inherited spend %.2f", got)
	}
}
