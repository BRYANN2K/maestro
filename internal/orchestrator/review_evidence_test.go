package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agent"
	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/settings"
)

type captureReviewLegacyAgent struct {
	prompt string
}

type incompleteReviewLegacyAgent struct{}

func (*incompleteReviewLegacyAgent) Name() string     { return "incomplete-review" }
func (*incompleteReviewLegacyAgent) Models() []string { return []string{"test-model"} }
func (*incompleteReviewLegacyAgent) Execute(_ context.Context, _ string, _ agent.Options) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 1)
	ch <- agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvTextDelta, agentcore.TextDelta{Text: "[pass] partial output"})
	close(ch)
	return ch, nil
}

type malformedDoneReviewLegacyAgent struct{}

func (*malformedDoneReviewLegacyAgent) Name() string     { return "malformed-done-review" }
func (*malformedDoneReviewLegacyAgent) Models() []string { return []string{"test-model"} }
func (*malformedDoneReviewLegacyAgent) Execute(_ context.Context, _ string, _ agent.Options) (<-chan agentcore.StreamEvent, error) {
	ch := make(chan agentcore.StreamEvent, 2)
	ch <- agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvTextDelta, agentcore.TextDelta{Text: "[pass] partial output"})
	ch <- agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvDone, "not a Done payload")
	close(ch)
	return ch, nil
}

func (a *captureReviewLegacyAgent) Name() string     { return "capture-review" }
func (a *captureReviewLegacyAgent) Models() []string { return []string{"test-model"} }
func (a *captureReviewLegacyAgent) Execute(_ context.Context, task string, _ agent.Options) (<-chan agentcore.StreamEvent, error) {
	a.prompt = task
	ch := make(chan agentcore.StreamEvent, 2)
	ch <- agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvTextDelta, agentcore.TextDelta{Text: "[pass] reviewed complete evidence"})
	ch <- agentcore.NewEvent(nil, agentcore.RoleReviewer, agentcore.EvDone, agentcore.Done{})
	close(ch)
	return ch, nil
}

func TestLegacyReviewerReceivesUntrackedWorktreeEvidence(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	if err := os.WriteFile(filepath.Join(dir, "fresh_untracked.go"), []byte("package main\n\nfunc fresh() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	capture := &captureReviewLegacyAgent{}
	runner := &legacyRunner{agent: capture, model: "test-model", o: orch, silent: true}
	result, err := runner.Run(t.Context(), agentcore.RoleReviewer, "Review the supplied evidence.")
	if err != nil {
		t.Fatal(err)
	}
	if !result.OK {
		t.Fatalf("review result = %+v", result)
	}
	for _, want := range []string{
		"=== FILE:",
		"spec.md",
		"design.md",
		"tasks.md",
		"=== COMPLETE GIT WORKTREE DIFF (TRACKED + UNTRACKED) ===",
		"fresh_untracked.go",
		"func fresh() {}",
	} {
		if !strings.Contains(capture.prompt, want) {
			t.Errorf("legacy reviewer prompt missing %q:\n%s", want, capture.prompt)
		}
	}
}

func TestLegacyReviewerFailsClosedWhenStreamEndsWithoutDone(t *testing.T) {
	orch, _ := newAcceptedContractPipeline(t)
	runner := &legacyRunner{agent: &incompleteReviewLegacyAgent{}, model: "test-model", o: orch, silent: true}
	orch.runner = runner

	items := orch.agentReview(t.Context())
	if level := (Verdict{Items: items}).VerdictLevel(); level != "fail" {
		t.Fatalf("agentReview = %#v, verdict %q; want fail", items, level)
	}
	if len(items) != 1 || !strings.Contains(items[0].Message, "without a completion event") {
		t.Fatalf("agentReview = %#v, want explicit truncated-stream failure", items)
	}
}

func TestLegacyReviewerFailsClosedOnMalformedDoneEvent(t *testing.T) {
	orch, _ := newAcceptedContractPipeline(t)
	runner := &legacyRunner{agent: &malformedDoneReviewLegacyAgent{}, model: "test-model", o: orch, silent: true}
	orch.runner = runner

	items := orch.agentReview(t.Context())
	if level := (Verdict{Items: items}).VerdictLevel(); level != "fail" {
		t.Fatalf("agentReview = %#v, verdict %q; want fail", items, level)
	}
	if len(items) != 1 || !strings.Contains(items[0].Message, "malformed completion payload") {
		t.Fatalf("agentReview = %#v, want explicit malformed-stream failure", items)
	}
}

func TestReviewPersistsBlockingFailureForUnstructuredLegacyOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX legacy-agent shim")
	}
	orch, _ := newAcceptedContractPipeline(t)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		return successfulDevResult(role), nil
	})
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}

	binDir := t.TempDir()
	shim := filepath.Join(binDir, "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"agent_message","text":"Everything looks fine."}'
`
	if err := os.WriteFile(shim, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	orch.runner = nil
	orch.settings.RoleDefaults[string(agentcore.RoleReviewer)] = settings.RoleDefaults{
		Engine: "legacy",
		Agent:  "codex",
		Model:  "test-model",
	}

	verdict, err := orch.Review(t.Context())
	if err == nil || verdict.VerdictLevel() != "fail" {
		t.Fatalf("Review = %+v, %v; want blocking failure", verdict, err)
	}
	review := orch.Session().Review
	if review == nil || review.Level != "fail" || review.Fingerprint != "" || !strings.Contains(review.Findings, "no structured findings") {
		t.Fatalf("persisted review = %+v, want non-archivable fail", review)
	}
	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true}); err == nil {
		t.Fatal("archive accepted an unstructured reviewer result")
	}
}
