package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/spec"
)

func TestAcceptRollbackPreservesConcurrentSpecFileInEveryWorkspaceMode(t *testing.T) {
	for _, kind := range []string{"stay", "branch", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			dir := newTestRepo(t)
			orch := newTestOrch(t, dir, &fakeRunner{})
			workspace := orch.workspaceRoute()
			state := acceptRollbackState{
				workspace:   workspace,
				targetGit:   workspace.git,
				targetStore: workspace.store,
				choice:      BranchChoice{Kind: kind},
				targetDir:   dir,
			}
			if kind != "stay" {
				state.choice.Name = "feat-rollback-" + kind
				state.originalBranch = "main"
			}
			if kind == "branch" {
				if err := workspace.git.Branch(t.Context(), state.choice.Name); err != nil {
					t.Fatal(err)
				}
				oid, err := workspace.git.BranchOID(t.Context(), state.choice.Name)
				if err != nil {
					t.Fatal(err)
				}
				state.branchOID = oid
			}
			if kind == "worktree" {
				worktree := filepath.Join(t.TempDir(), "worktree")
				if err := workspace.git.WorktreeAdd(t.Context(), worktree, state.choice.Name); err != nil {
					t.Fatal(err)
				}
				oid, err := workspace.git.BranchOID(t.Context(), state.choice.Name)
				if err != nil {
					t.Fatal(err)
				}
				state.branchOID = oid
				state.targetDir = worktree
				state.targetGit = git.New(worktree)
				state.targetStore = spec.NewStore(filepath.Join(worktree, "specs"))
			}

			s := testRollbackSpec()
			materialization, err := state.targetStore.WriteTrioTracked(t.Context(), s, "design", "tasks")
			if err != nil {
				t.Fatal(err)
			}
			state.trio = materialization
			concurrent := state.targetStore.PathFor(s.ID, "concurrent.txt")
			if err := os.WriteFile(concurrent, []byte("preserve me\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			err = rollbackAcceptMaterialization(state)
			if err == nil || !strings.Contains(err.Error(), "preserve changed spec trio") {
				t.Fatalf("rollback error = %v, want changed-trio refusal", err)
			}
			if got, readErr := os.ReadFile(concurrent); readErr != nil || string(got) != "preserve me\n" {
				t.Fatalf("concurrent file = %q, %v", got, readErr)
			}
			for _, name := range []string{spec.FileSpec, spec.FileDesign, spec.FileTasks} {
				if _, statErr := os.Stat(state.targetStore.PathFor(s.ID, name)); statErr != nil {
					t.Fatalf("created %s was partially removed: %v", name, statErr)
				}
			}
			if kind == "branch" {
				if current := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); current != state.choice.Name {
					t.Fatalf("changed branch was switched away: %q", current)
				}
			}
			if kind == "worktree" {
				if _, statErr := os.Stat(state.targetDir); statErr != nil {
					t.Fatalf("changed worktree was removed: %v", statErr)
				}
			}
		})
	}
}

func TestAcceptRollbackPreservesDirtyWorktreeOutsideTrio(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	workspace := orch.workspaceRoute()
	branch := "feat-dirty-worktree-rollback"
	worktree := filepath.Join(t.TempDir(), "worktree")
	if err := workspace.git.WorktreeAdd(t.Context(), worktree, branch); err != nil {
		t.Fatal(err)
	}
	oid, err := workspace.git.BranchOID(t.Context(), branch)
	if err != nil {
		t.Fatal(err)
	}
	targetStore := spec.NewStore(filepath.Join(worktree, "specs"))
	s := testRollbackSpec()
	materialization, err := targetStore.WriteTrioTracked(t.Context(), s, "design", "tasks")
	if err != nil {
		t.Fatal(err)
	}
	concurrent := filepath.Join(worktree, "concurrent.txt")
	if err := os.WriteFile(concurrent, []byte("preserve me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = rollbackAcceptMaterialization(acceptRollbackState{
		workspace: workspace, targetGit: git.New(worktree), targetStore: targetStore, trio: materialization,
		choice: BranchChoice{Kind: "worktree", Name: branch}, originalBranch: "main",
		targetDir: worktree, branchOID: oid,
	})
	if err == nil || !strings.Contains(err.Error(), "preserve dirty worktree") {
		t.Fatalf("rollback error = %v, want dirty-worktree refusal", err)
	}
	if got, readErr := os.ReadFile(concurrent); readErr != nil || string(got) != "preserve me\n" {
		t.Fatalf("concurrent worktree file = %q, %v", got, readErr)
	}
	if _, statErr := os.Stat(worktree); statErr != nil {
		t.Fatalf("dirty worktree was removed: %v", statErr)
	}
	if got, oidErr := workspace.git.BranchOID(t.Context(), branch); oidErr != nil || got != oid {
		t.Fatalf("dirty worktree branch = %q, %v; want %q", got, oidErr, oid)
	}
}

func TestAcceptRollbackCASPreservesConcurrentBranchCommit(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	workspace := orch.workspaceRoute()
	branch := "feat-concurrent-rollback"
	if err := workspace.git.Branch(t.Context(), branch); err != nil {
		t.Fatal(err)
	}
	createdOID, err := workspace.git.BranchOID(t.Context(), branch)
	if err != nil {
		t.Fatal(err)
	}
	s := testRollbackSpec()
	materialization, err := workspace.store.WriteTrioTracked(t.Context(), s, "design", "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "concurrent.txt"), []byte("preserve commit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "concurrent.txt")
	gitRun(t, dir, "commit", "-m", "concurrent commit")
	concurrentOID, err := workspace.git.BranchOID(t.Context(), branch)
	if err != nil || concurrentOID == createdOID {
		t.Fatalf("concurrent OID = %q, %v; created %q", concurrentOID, err, createdOID)
	}

	err = rollbackAcceptMaterialization(acceptRollbackState{
		workspace: workspace, targetGit: workspace.git, targetStore: workspace.store, trio: materialization,
		choice: BranchChoice{Kind: "branch", Name: branch}, originalBranch: "main",
		targetDir: dir, branchOID: createdOID,
	})
	if err == nil || !strings.Contains(err.Error(), "preserve concurrently changed branch") {
		t.Fatalf("rollback error = %v, want concurrent-commit refusal", err)
	}
	if current := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); current != branch {
		t.Fatalf("concurrently committed branch was switched away: %q", current)
	}
	if got, oidErr := workspace.git.BranchOID(t.Context(), branch); oidErr != nil || got != concurrentOID {
		t.Fatalf("concurrent branch = %q, %v; want %q", got, oidErr, concurrentOID)
	}
	if got, readErr := os.ReadFile(filepath.Join(dir, "concurrent.txt")); readErr != nil || string(got) != "preserve commit\n" {
		t.Fatalf("concurrent commit file = %q, %v", got, readErr)
	}
}

func TestAcceptRollbackNeverFollowsReplacementSymlink(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	workspace := orch.workspaceRoute()
	s := testRollbackSpec()
	materialization, err := workspace.store.WriteTrioTracked(t.Context(), s, "design", "tasks")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(workspace.store.Path(s.ID)); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, workspace.store.Path(s.ID)); err != nil {
		t.Fatal(err)
	}
	err = rollbackAcceptMaterialization(acceptRollbackState{
		workspace: workspace, targetGit: workspace.git, targetStore: workspace.store, trio: materialization,
		choice: BranchChoice{Kind: "stay"}, targetDir: dir,
	})
	if err == nil {
		t.Fatal("rollback followed a replacement spec-directory symlink")
	}
	if got, readErr := os.ReadFile(sentinel); readErr != nil || string(got) != "unchanged" {
		t.Fatalf("outside sentinel = %q, %v", got, readErr)
	}
}

func testRollbackSpec() *spec.Spec {
	return &spec.Spec{
		ID: "rollback-safety", Title: "Rollback safety", Status: spec.StatusProposal, Category: "feat",
		Body: "# Goal\n\nTest exact rollback.\n",
	}
}
