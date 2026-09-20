package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	cfg := &config.Config{Options: map[string]string{"budget-max-usd": "1", "budget-max-daily-usd": "0.5"}}
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
	if got := orch.guardrails.Budget.Daily(); got != 0.25 {
		t.Fatalf("second run lost daily spend %.2f", got)
	}
	kill, _ := orch.guardrails.Budget.Track(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvDone, agentcore.Done{Cost: &agentcore.Cost{InputUSD: 0.25}}))
	if !kill {
		t.Fatal("daily budget did not stop cumulative spend at the configured limit")
	}
}

func TestDailyBudgetSurvivesOrchestratorReopen(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	cfg := &config.Config{Options: map[string]string{"budget-max-daily-usd": "1"}}
	first, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: sessionsDir, Config: cfg, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	first.BudgetState().Track(agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvDone, agentcore.Done{
		Cost: &agentcore.Cost{InputUSD: 0.25},
	}))
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: sessionsDir, Config: cfg, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if got := second.BudgetState().Daily(); got != 0.25 {
		t.Fatalf("reopened daily spend = %.2f, want .25", got)
	}
}

func TestDailyBudgetFinalizationUsesDetachedBoundedContext(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{Options: map[string]string{"budget-max-daily-usd": "1"}}
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"), Config: cfg, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orch.Close() })
	budget := orch.BudgetState()
	token, err := budget.ReserveEstimate(t.Context(), 0.20)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	decision := budget.TrackReservation(canceled, agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvDone, agentcore.Done{
		Cost: &agentcore.Cost{InputUSD: 0.10},
	}), token)
	if decision.Kill || !decision.AccountDone || budget.Err() != nil || budget.Daily() != 0.10 {
		t.Fatalf("settle after cancellation: decision=%+v daily=%.2f err=%v", decision, budget.Daily(), budget.Err())
	}

	releasedToken, err := budget.ReserveEstimate(t.Context(), 0.20)
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.ReleaseReservation(canceled, releasedToken); err != nil {
		t.Fatalf("release after cancellation: %v", err)
	}
	if err := budget.CheckEstimate(0.70); err != nil {
		t.Fatalf("detached release left a durable liability: %v", err)
	}
}

func TestBudgetWallClockIsARealRunDeadline(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{Options: map[string]string{"budget-max-wall-clock": "20ms"}}
	orch, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"), Config: cfg, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orch.Close() })
	ctx, cancel := orch.bindBudgetKill(t.Context())
	defer cancel()
	select {
	case <-ctx.Done():
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("deadline error = %v", ctx.Err())
		}
	case <-time.After(time.Second):
		t.Fatal("silent run ignored budget wall-clock deadline")
	}
}
