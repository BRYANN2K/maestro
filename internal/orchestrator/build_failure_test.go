package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
)

func TestBuildFailsClosedOnUnsuccessfulAgentResult(t *testing.T) {
	dir := newTestRepo(t)
	runner := runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		return agentcore.AgentResult{Role: string(role), OK: false, Summary: "implementation failed"}, nil
	})
	orch := newTestOrch(t, dir, runner)
	if _, err := orch.Propose(t.Context(), "Add a health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}

	err := orch.Build(t.Context(), BuildOptions{})
	var streamErr agentcore.StreamError
	if err == nil || !errors.As(err, &streamErr) || !strings.Contains(err.Error(), "implementation failed") {
		t.Fatalf("Build error = %v, want typed unsuccessful-result failure", err)
	}
	if orch.Phase() != session.PhaseSpec {
		t.Fatalf("failed build phase = %q, want %q", orch.Phase(), session.PhaseSpec)
	}
}

func TestFixFailsClosedOnUnsuccessfulAgentResult(t *testing.T) {
	dir := newTestRepo(t)
	runner := runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		return agentcore.AgentResult{Role: string(role), OK: false, Summary: "fix failed"}, nil
	})
	orch := newTestOrch(t, dir, runner)
	if _, err := orch.Propose(t.Context(), "Add a health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.ensureSpecContract(); err != nil {
		t.Fatal(err)
	}
	orch.sess.Phase = session.PhaseReview
	orch.sess.Review = &session.ReviewResult{Level: "warn", Findings: "- [warn] incomplete behavior\n"}
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}

	err := orch.Fix(t.Context())
	var streamErr agentcore.StreamError
	if err == nil || !errors.As(err, &streamErr) || !strings.Contains(err.Error(), "fix failed") {
		t.Fatalf("Fix error = %v, want typed unsuccessful-result failure", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("failed fix phase = %q, want %q", orch.Phase(), session.PhaseReview)
	}
}
