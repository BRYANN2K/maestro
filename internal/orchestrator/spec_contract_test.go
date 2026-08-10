package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

func newAcceptedContractPipeline(t *testing.T) (*Orchestrator, string) {
	t.Helper()
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, nil)
	if _, err := orch.Propose(t.Context(), "Add a deterministic health helper"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	return orch, dir
}

func newContractTestOrchestrator(t *testing.T, dir, sessionsDir string, runner Runner) *Orchestrator {
	t.Helper()
	t.Setenv("MAESTRO_MEMORY_DIR", filepath.Join(t.TempDir(), "memory"))
	t.Setenv("MAESTRO_CHECKPOINTS_DIR", filepath.Join(t.TempDir(), "checkpoints"))
	orch, err := New(t.Context(), Options{
		ProjectDir:  dir,
		SessionsDir: sessionsDir,
		In:          strings.NewReader("n\n"),
		Out:         &bytes.Buffer{},
		Runner:      runner,
	})
	if err != nil {
		t.Fatal(err)
	}
	return orch
}

func successfulDevResult(role agentcore.Role) agentcore.AgentResult {
	return agentcore.AgentResult{Role: string(role), OK: true, Summary: "implemented and verified"}
}

func TestNormalizeTaskCheckboxesPreservesEveryOtherByte(t *testing.T) {
	input := []byte("# Tasks\r\n\r\n- [ ] first  \r\n\t- [x] second\ntext with [x] inline\n")
	want := "# Tasks\r\n\r\n- [ ] first  \r\n\t- [ ] second\ntext with [x] inline\n"
	normalized, states, err := normalizeTaskCheckboxes(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(normalized) != want {
		t.Fatalf("normalized tasks = %q, want %q", normalized, want)
	}
	if len(states) != 2 || states[0] || !states[1] {
		t.Fatalf("task states = %v, want [false true]", states)
	}
	if _, _, err := normalizeTaskCheckboxes([]byte("- [X] unsupported\n")); err == nil {
		t.Fatal("uppercase checkbox marker was accepted")
	}
}

func TestBuildLeavesCheckboxProgressPendingUntilReview(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	tasksPath := orch.store.PathFor(orch.spec.ID, spec.FileTasks)
	specBefore, err := os.ReadFile(orch.store.PathFor(orch.spec.ID, spec.FileSpec))
	if err != nil {
		t.Fatal(err)
	}
	designBefore, err := os.ReadFile(orch.store.PathFor(orch.spec.ID, spec.FileDesign))
	if err != nil {
		t.Fatal(err)
	}
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		updated := strings.Replace(string(data), "- [ ]", "- [x]", 1)
		if err := os.WriteFile(tasksPath, []byte(updated), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "health.go"), []byte("package main\n"), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})

	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatalf("Build: %v", err)
	}
	contract := orch.sess.SpecContract
	if contract == nil || len(contract.TaskStates) < 1 || contract.TaskStates[0] {
		t.Fatalf("persisted spec contract = %+v, want first dev-reported task still pending", contract)
	}
	if _, err := orch.validatePendingSpecContract(); err != nil {
		t.Fatalf("pending monotonic task progress rejected before review: %v", err)
	}
	if err := orch.validateSpecContract(); err == nil || !strings.Contains(err.Error(), "outside a successful review") {
		t.Fatalf("exact contract before review = %v, want pending-state refusal", err)
	}
	if got, err := os.ReadFile(orch.store.PathFor(orch.spec.ID, spec.FileSpec)); err != nil || string(got) != string(specBefore) {
		t.Fatalf("spec.md changed: %v", err)
	}
	if got, err := os.ReadFile(orch.store.PathFor(orch.spec.ID, spec.FileDesign)); err != nil || string(got) != string(designBefore) {
		t.Fatalf("design.md changed: %v", err)
	}
}

func TestBuildRejectsEditsMadeAfterAcceptBeforeFirstBuild(t *testing.T) {
	tests := []struct {
		name string
		file string
		edit func([]byte) []byte
		want string
	}{
		{name: "spec", file: spec.FileSpec, edit: func(data []byte) []byte { return append(data, []byte("\nmanual spec edit\n")...) }, want: "spec.md was modified"},
		{name: "design", file: spec.FileDesign, edit: func(data []byte) []byte { return append(data, []byte("\nmanual design edit\n")...) }, want: "design.md was modified"},
		{name: "task text", file: spec.FileTasks, edit: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), "Tests for T1", "rewritten task", 1))
		}, want: "tasks.md structure or text changed"},
		{name: "task checkbox", file: spec.FileTasks, edit: func(data []byte) []byte {
			return []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1))
		}, want: "checkbox 1 changed outside a successful review"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orch, _ := newAcceptedContractPipeline(t)
			path := orch.store.PathFor(orch.spec.ID, tt.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, tt.edit(data), 0o644); err != nil {
				t.Fatal(err)
			}
			called := false
			orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
				called = true
				return successfulDevResult(role), nil
			})
			err = orch.Build(t.Context(), BuildOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build error = %v, want %q", err, tt.want)
			}
			if called {
				t.Fatal("dev runner started before the persisted spec contract was checked")
			}
			if orch.Phase() != session.PhaseSpec {
				t.Fatalf("phase = %q, want spec", orch.Phase())
			}
		})
	}
}

func TestAcceptContractPersistsAndReloads(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	orch := newContractTestOrchestrator(t, dir, sessionsDir, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Add a durable health helper"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if orch.sess.SpecContract == nil {
		t.Fatal("Accept did not persist a spec contract")
	}
	want := *orch.sess.SpecContract
	want.TaskStates = append([]bool(nil), orch.sess.SpecContract.TaskStates...)

	restored := newContractTestOrchestrator(t, dir, sessionsDir, &fakeRunner{})
	if restored.Phase() != session.PhaseSpec || restored.spec == nil {
		t.Fatalf("restored lifecycle = phase %q spec %+v", restored.Phase(), restored.spec)
	}
	if restored.sess.SpecContract == nil || !reflect.DeepEqual(*restored.sess.SpecContract, want) {
		t.Fatalf("restored contract = %+v, want %+v", restored.sess.SpecContract, want)
	}
	if err := restored.ensureSpecContract(); err != nil {
		t.Fatalf("restored accepted contract does not validate: %v", err)
	}
}

func TestAcceptCapturesContractForEveryWorkspaceMode(t *testing.T) {
	for _, choice := range []BranchChoice{
		{Kind: "stay"},
		{Kind: "branch", Name: "feat-contract-branch"},
		{Kind: "worktree", Name: "feat-contract-worktree"},
	} {
		t.Run(choice.Kind, func(t *testing.T) {
			dir := newTestRepo(t)
			orch := newContractTestOrchestrator(t, dir, filepath.Join(t.TempDir(), "sessions"), &fakeRunner{})
			if _, err := orch.Propose(t.Context(), "Add a workspace contract helper"); err != nil {
				t.Fatal(err)
			}
			if _, err := orch.Accept(t.Context(), choice); err != nil {
				t.Fatal(err)
			}
			if orch.sess.SpecContract == nil || orch.sess.SpecContract.SpecID != orch.spec.ID {
				t.Fatalf("accepted contract = %+v", orch.sess.SpecContract)
			}
			if err := orch.validateSpecContract(); err != nil {
				t.Fatalf("accepted %s contract: %v", choice.Kind, err)
			}
		})
	}
}

func TestAcceptPersistenceFailureRollsBackEveryWorkspaceMode(t *testing.T) {
	for _, choice := range []BranchChoice{
		{Kind: "stay"},
		{Kind: "branch", Name: "feat-contract-branch-rollback"},
		{Kind: "worktree", Name: "feat-contract-worktree-rollback"},
	} {
		t.Run(choice.Kind, func(t *testing.T) {
			dir := newTestRepo(t)
			sessionsDir := filepath.Join(t.TempDir(), "sessions")
			orch := newContractTestOrchestrator(t, dir, sessionsDir, &fakeRunner{})
			if _, err := orch.Propose(t.Context(), "Add a rollback-safe contract helper"); err != nil {
				t.Fatal(err)
			}
			specID := orch.sess.Draft.ID
			if err := os.RemoveAll(sessionsDir); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sessionsDir, []byte("block session persistence\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := orch.Accept(t.Context(), choice)
			if err == nil || !strings.Contains(err.Error(), "persist accepted spec contract") {
				t.Fatalf("Accept error = %v, want persistence refusal", err)
			}
			if orch.Phase() != session.PhasePropose || orch.sess.Draft == nil || orch.sess.SpecContract != nil {
				t.Fatalf("in-memory lifecycle was partially published: %+v", orch.sess)
			}
			targetDir := dir
			if choice.Kind == "worktree" {
				targetDir = filepath.Join(filepath.Dir(dir), choice.Name)
			}
			if _, statErr := os.Stat(filepath.Join(targetDir, "specs", specID)); !os.IsNotExist(statErr) {
				t.Fatalf("spec trio survived failed accept: %v", statErr)
			}
			if choice.Kind == "worktree" {
				if _, statErr := os.Stat(targetDir); !os.IsNotExist(statErr) {
					t.Fatalf("worktree survived failed accept: %v", statErr)
				}
			}
			if choice.Kind != "stay" && strings.TrimSpace(gitRun(t, dir, "branch", "--list", choice.Name)) != "" {
				t.Fatalf("branch %q survived failed accept", choice.Name)
			}
			if branch := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); branch != "main" {
				t.Fatalf("current branch = %q after rollback, want main", branch)
			}
		})
	}
}

func TestBuildRejectsLegacySessionWithoutAcceptedContract(t *testing.T) {
	orch, _ := newAcceptedContractPipeline(t)
	orch.sess.SpecContract = nil
	called := false
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		called = true
		return successfulDevResult(role), nil
	})
	err := orch.Build(t.Context(), BuildOptions{})
	if err == nil || !strings.Contains(err.Error(), "restored legacy session has no accepted-trio baseline") {
		t.Fatalf("Build error = %v, want explicit legacy-session refusal", err)
	}
	if called {
		t.Fatal("legacy session ran the dev agent without an accepted baseline")
	}
}

func TestBuildRejectsSpecTrioRewrites(t *testing.T) {
	tests := []struct {
		name string
		edit func(*Orchestrator) error
		want string
	}{
		{
			name: "spec modified",
			edit: func(o *Orchestrator) error {
				return os.WriteFile(o.store.PathFor(o.spec.ID, spec.FileSpec), []byte("rewritten\n"), 0o644)
			},
			want: "modified spec.md",
		},
		{
			name: "design deleted",
			edit: func(o *Orchestrator) error {
				return os.Remove(o.store.PathFor(o.spec.ID, spec.FileDesign))
			},
			want: "inspect design.md",
		},
		{
			name: "task text rewritten",
			edit: func(o *Orchestrator) error {
				path := o.store.PathFor(o.spec.ID, spec.FileTasks)
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				return os.WriteFile(path, []byte(strings.Replace(string(data), "Tests for T1", "different task text", 1)), 0o644)
			},
			want: "rewrote or removed tasks.md content",
		},
		{
			name: "tasks deleted",
			edit: func(o *Orchestrator) error {
				return os.Remove(o.store.PathFor(o.spec.ID, spec.FileTasks))
			},
			want: "inspect tasks.md",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			orch, _ := newAcceptedContractPipeline(t)
			orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
				if err := tt.edit(orch); err != nil {
					return agentcore.AgentResult{}, err
				}
				return successfulDevResult(role), nil
			})
			err := orch.Build(t.Context(), BuildOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Build error = %v, want %q", err, tt.want)
			}
			if orch.Phase() != session.PhaseSpec {
				t.Fatalf("phase = %q, want spec after rejected build", orch.Phase())
			}
			if orch.sess.SpecContract == nil {
				t.Fatal("accepted-trio baseline was not persisted before the dev run")
			}
		})
	}
}

func TestReviewAuthorizesPendingCheckboxProgress(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	orch.runner = &fakeRunner{Wd: func() string { return orch.WorkDirDisplay() }}
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	path := orch.store.PathFor(orch.spec.ID, spec.FileTasks)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	verdict, err := orch.Review(t.Context())
	if err != nil || verdict.VerdictLevel() == "fail" {
		t.Fatalf("Review = %+v, %v, want pending progress accepted by gates", verdict, err)
	}
	if orch.sess.SpecContract == nil || !orch.sess.SpecContract.TaskStates[0] {
		t.Fatalf("passing review did not authorize task progress: %+v", orch.sess.SpecContract)
	}
	if err := orch.validateSpecContract(); err != nil {
		t.Fatalf("review-authorized contract is not exact: %v", err)
	}
	if orch.sess.Review == nil || orch.sess.Review.Fingerprint == "" {
		t.Fatalf("passing review did not persist a release fingerprint: %+v", orch.sess.Review)
	}
	restored := newContractTestOrchestrator(t, dir, orch.sessions.Dir(), &fakeRunner{})
	if restored.Phase() != session.PhaseReview || restored.sess.SpecContract == nil || !restored.sess.SpecContract.TaskStates[0] {
		t.Fatalf("reloaded reviewed task authorization = phase %q contract %+v", restored.Phase(), restored.sess.SpecContract)
	}
	if err := restored.validateSpecContract(); err != nil {
		t.Fatalf("reloaded reviewed contract is not exact: %v", err)
	}
}

func TestFailedReviewDoesNotAuthorizeRunnerReportedCheckboxes(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	tasksPath := orch.store.PathFor(orch.spec.ID, spec.FileTasks)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(tasksPath, []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		// The runner claims success despite leaving code that cannot pass even
		// the deterministic syntax gates.
		if err := os.WriteFile(filepath.Join(dir, "dishonest.go"), []byte("package main\n\nfunc broken("), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})

	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatalf("Build accepted runner result: %v", err)
	}
	if orch.sess.SpecContract.TaskStates[0] {
		t.Fatal("Build authorized a runner-reported checkbox before review")
	}
	verdict, err := orch.Review(t.Context())
	if err == nil || verdict.VerdictLevel() != "fail" {
		t.Fatalf("Review = %+v, %v, want deterministic failure", verdict, err)
	}
	if orch.sess.SpecContract.TaskStates[0] {
		t.Fatal("failed review authorized a pending checkbox")
	}
	if orch.sess.Review == nil || orch.sess.Review.Fingerprint != "" {
		t.Fatalf("failed review persisted release authority: %+v", orch.sess.Review)
	}
	if _, pendingErr := orch.validatePendingSpecContract(); pendingErr != nil {
		t.Fatalf("failed review destroyed repairable pending progress: %v", pendingErr)
	}

	// Even a forged lifecycle phase cannot make the pending task state pass
	// the independent Archive/Docs exact-contract gate.
	orch.sess.Phase = session.PhaseReview
	if _, _, docsErr := orch.DocsDraft(t.Context()); docsErr == nil || !strings.Contains(docsErr.Error(), "outside a successful review") {
		t.Fatalf("DocsDraft error = %v, want unreviewed-checkbox refusal", docsErr)
	}
	archiveErr := orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if archiveErr == nil || !strings.Contains(archiveErr.Error(), "outside a successful review") {
		t.Fatalf("Archive error = %v, want unreviewed-checkbox refusal", archiveErr)
	}
}

func TestFixCanExtendPendingProgressBeforePassingReview(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	tasksPath := orch.store.PathFor(orch.spec.ID, spec.FileTasks)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(tasksPath, []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package main\n\nfunc broken("), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err == nil {
		t.Fatal("Review unexpectedly passed broken implementation")
	}
	sessionsDir := orch.sessions.Dir()
	orch = newContractTestOrchestrator(t, dir, sessionsDir, &fakeRunner{})
	if orch.Phase() != session.PhaseBuild || orch.sess.Review == nil || orch.sess.Review.Level != "fail" {
		t.Fatalf("reloaded failed review = phase %q review %+v", orch.Phase(), orch.sess.Review)
	}
	if _, err := orch.validatePendingSpecContract(); err != nil {
		t.Fatalf("reloaded pending progress is not repairable: %v", err)
	}
	tasksPath = orch.store.PathFor(orch.spec.ID, spec.FileTasks)

	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(tasksPath, []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.Remove(filepath.Join(dir, "broken.go")); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "health.go"), []byte("package main\n\nfunc health() bool { return true }\n"), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})
	if err := orch.Fix(t.Context()); err != nil {
		t.Fatalf("Fix with monotonic pending progress: %v", err)
	}
	if len(orch.sess.SpecContract.TaskStates) < 2 || orch.sess.SpecContract.TaskStates[0] || orch.sess.SpecContract.TaskStates[1] {
		t.Fatalf("Fix prematurely authorized pending progress: %+v", orch.sess.SpecContract)
	}
	pending, err := orch.validatePendingSpecContract()
	if err != nil || len(pending.taskStates) < 2 || !pending.taskStates[0] || !pending.taskStates[1] {
		t.Fatalf("pending state after Fix = %+v, %v, want two monotonic checks", pending, err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatalf("Review repaired pending progress: %v", err)
	}
	if !orch.sess.SpecContract.TaskStates[0] || !orch.sess.SpecContract.TaskStates[1] {
		t.Fatalf("passing review did not authorize repaired progress: %+v", orch.sess.SpecContract)
	}
}

func TestFixCannotRegressCheckboxPendingAfterFailedReview(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	tasksPath := orch.store.PathFor(orch.spec.ID, spec.FileTasks)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(tasksPath, []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "broken.go"), []byte("package main\n\nfunc broken("), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err == nil {
		t.Fatal("Review unexpectedly passed broken implementation")
	}

	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(tasksPath, []byte(strings.Replace(string(data), "- [x]", "- [ ]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})
	err := orch.Fix(t.Context())
	if err == nil || !strings.Contains(err.Error(), "reverted pending or review-authorized tasks.md checkbox 1") {
		t.Fatalf("Fix error = %v, want pending-regression refusal", err)
	}
	if orch.sess.SpecContract.TaskStates[0] {
		t.Fatal("rejected Fix mutated the durable task authorization")
	}
}

func TestReviewAuthorizationSaveFailureIsFailClosed(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	tasksPath := orch.store.PathFor(orch.spec.ID, spec.FileTasks)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(tasksPath)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(tasksPath, []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "health.go"), []byte("package main\n\nfunc health() bool { return true }\n"), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}

	sessionsDir := orch.sessions.Dir()
	backupDir := sessionsDir + "-before-review"
	if err := os.Rename(sessionsDir, backupDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionsDir, []byte("block session persistence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, reviewErr := orch.Review(t.Context())
	if err := os.Remove(sessionsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupDir, sessionsDir); err != nil {
		t.Fatal(err)
	}
	if reviewErr == nil || !strings.Contains(reviewErr.Error(), "persist verdict and task authorization") {
		t.Fatalf("Review error = %v, want atomic persistence refusal", reviewErr)
	}
	if orch.Phase() != session.PhaseBuild || orch.sess.Review != nil || orch.sess.SpecContract.TaskStates[0] {
		t.Fatalf("failed save leaked review authority in memory: phase %q review %+v contract %+v", orch.Phase(), orch.sess.Review, orch.sess.SpecContract)
	}

	restored := newContractTestOrchestrator(t, dir, sessionsDir, &fakeRunner{})
	if restored.Phase() != session.PhaseBuild || restored.sess.Review != nil || restored.sess.SpecContract.TaskStates[0] {
		t.Fatalf("failed save leaked review authority after reload: phase %q review %+v contract %+v", restored.Phase(), restored.sess.Review, restored.sess.SpecContract)
	}
}

func TestFixRejectsCompletedCheckboxRegression(t *testing.T) {
	orch, dir := newAcceptedContractPipeline(t)
	path := orch.store.PathFor(orch.spec.ID, spec.FileTasks)
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(data), "- [ ]", "- [x]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(filepath.Join(dir, "health.go"), []byte("package main\n"), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	orch.runner = runnerFunc(func(_ context.Context, role agentcore.Role, _ string) (agentcore.AgentResult, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return agentcore.AgentResult{}, err
		}
		if err := os.WriteFile(path, []byte(strings.Replace(string(data), "- [x]", "- [ ]", 1)), 0o644); err != nil {
			return agentcore.AgentResult{}, err
		}
		return successfulDevResult(role), nil
	})
	err := orch.Fix(t.Context())
	if err == nil || !strings.Contains(err.Error(), "reverted pending or review-authorized tasks.md checkbox 1") {
		t.Fatalf("Fix error = %v, want monotonic-checkbox refusal", err)
	}
	if orch.sess.SpecContract == nil || !orch.sess.SpecContract.TaskStates[0] {
		t.Fatalf("failed fix regressed persisted task state: %+v", orch.sess.SpecContract)
	}
}

func TestArchiveContractGateRejectsForgedCurrentReview(t *testing.T) {
	orch, _, _ := newReviewedPipeline(t)
	path := orch.store.PathFor(orch.spec.ID, spec.FileSpec)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("\nrewritten after review\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	// Even if an in-memory fingerprint is forged to the current tree, archive
	// must independently enforce the persisted accepted-trio contract.
	fingerprint, err := orch.worktreeFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	orch.sess.Review.Fingerprint = fingerprint
	err = orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "spec.md was modified") {
		t.Fatalf("Archive error = %v, want spec-contract refusal", err)
	}
}
