package orchestrator

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/session"
)

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func newReviewedPipeline(t *testing.T) (*Orchestrator, string, string) {
	t.Helper()
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Add a small health helper"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	if review := orch.Session().Review; review == nil || review.Level == "fail" || review.Fingerprint == "" {
		t.Fatalf("review gate = %+v, want passing result with fingerprint", review)
	}
	return orch, dir, orch.ActiveSpec().ID
}

func TestArchiveAcceptsUnchangedReviewedWorktree(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)

	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true}); err != nil {
		t.Fatalf("Archive unchanged reviewed worktree: %v", err)
	}
	if orch.Phase() != session.PhaseChat {
		t.Fatalf("phase = %q, want chat", orch.Phase())
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "archive", specID, "spec.md")); err != nil {
		t.Fatalf("archived spec missing: %v", err)
	}
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("archive commit left checkout or index dirty: %q", status)
	}
}

func TestArchiveRejectsPreexistingStagedIndexWithoutChangingIt(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	gitRun(t, dir, "add", "-A")
	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))

	err = orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "index already contains staged path") {
		t.Fatalf("Archive error = %v, want preexisting-index refusal", err)
	}
	indexAfter, readErr := os.ReadFile(indexPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("archive refusal modified the preexisting staged index")
	}
	if headAfter := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("archive refusal moved HEAD from %s to %s", headBefore, headAfter)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec moved on staged-index refusal: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestArchiveRollsBackAutosaveBetweenFinalCheckAndCommit(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	from := orch.Phase()
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}

	autosave := []byte("package main\n\nfunc main() { println(\"autosaved\") }\n")
	err = orch.publishArchiveTransactionWithGate(t.Context(), tx, func() error {
		return os.WriteFile(filepath.Join(dir, "main.go"), autosave, 0o644)
	})
	if err == nil || !strings.Contains(err.Error(), "worktree changed during archive") || !strings.Contains(err.Error(), "no unreviewed changes were committed") {
		t.Fatalf("publish error = %v, want concurrent-autosave refusal", err)
	}
	if tx.published {
		t.Fatal("concurrent autosave left the prepared commit published")
	}
	if err := orch.rollbackArchive(t.Context(), from); err != nil {
		t.Fatal(err)
	}

	if headAfter := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("concurrent autosave moved HEAD from %s to %s", headBefore, headAfter)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("concurrent autosave rollback modified the real Git index")
	}
	if _, err := os.Stat(indexPath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("concurrent autosave left an index lock: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "main.go")); err != nil || string(got) != string(autosave) {
		t.Fatalf("concurrent user edit was not preserved: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec was not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "archive", specID)); !os.IsNotExist(err) {
		t.Fatalf("archive destination remains after rollback: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestArchiveBypassesCommitAndReferenceHooks(t *testing.T) {
	orch, dir, _ := newReviewedPipeline(t)
	hooksDir := filepath.Join(dir, ".git", "hooks")
	preCommit := "#!/bin/sh\nprintf ran > pre-commit-ran\nprintf 'package main\\n' > hook_payload.go\ngit add hook_payload.go\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(preCommit), 0o755); err != nil {
		t.Fatal(err)
	}
	referenceTransaction := "#!/bin/sh\nprintf ran > reference-transaction-ran\nprintf 'package main\\n' > ref_hook_payload.go\ngit add ref_hook_payload.go\n"
	if err := os.WriteFile(filepath.Join(hooksDir, "reference-transaction"), []byte(referenceTransaction), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true}); err != nil {
		t.Fatalf("Archive with malicious hooks: %v", err)
	}
	for _, name := range []string{"pre-commit-ran", "hook_payload.go", "reference-transaction-ran", "ref_hook_payload.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("disabled Git hook created %s: %v", name, err)
		}
	}
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("hook-safe archive left checkout dirty: %q", status)
	}
}

func TestArchiveCommitsReviewedSpecWhenArchiveDirectoryIsIgnored(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("/specs/archive/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true}); err != nil {
		t.Fatalf("Archive with ignored destination: %v", err)
	}
	gitRun(t, dir, "cat-file", "-e", "HEAD:specs/archive/"+specID+"/spec.md")
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("ignored-destination archive left checkout dirty: %q", status)
	}
}

func TestArchiveRejectsBranchSwitchAfterPreparation(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "branch", "peer", headBefore)
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	gitRun(t, dir, "switch", "peer")
	from := orch.Phase()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}

	err = orch.publishArchiveTransaction(t.Context(), tx)
	if err == nil || (!strings.Contains(err.Error(), "workspace identity changed") && !strings.Contains(err.Error(), "index changed")) {
		t.Fatalf("publish error = %v, want concurrent-branch-switch refusal", err)
	}
	if tx.published {
		t.Fatal("branch-switch refusal published the prepared commit")
	}
	if err := orch.rollbackArchive(t.Context(), from); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); got != "peer" {
		t.Fatalf("branch-switch refusal changed current branch to %q", got)
	}
	for _, branch := range []string{"main", "peer"} {
		if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", branch)); got != headBefore {
			t.Fatalf("branch %s moved from %s to %s", branch, headBefore, got)
		}
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("branch-switch refusal modified the Git index")
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("branch-switch refusal did not restore active spec: %v", err)
	}
}

func TestArchiveRejectsSameCommitBranchSwitchAfterReview(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	gitRun(t, dir, "branch", "peer", headBefore)
	gitRun(t, dir, "switch", "peer")
	statusBefore := gitRun(t, dir, "status", "--porcelain=v1", "-z")

	err := orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || (!strings.Contains(err.Error(), "active branch") && !strings.Contains(err.Error(), "Git ref or HEAD changed")) {
		t.Fatalf("Archive error = %v, want reviewed-branch refusal", err)
	}
	if statusAfter := gitRun(t, dir, "status", "--porcelain=v1", "-z"); statusAfter != statusBefore {
		t.Fatalf("branch-identity refusal changed checkout:\nbefore %q\nafter  %q", statusBefore, statusAfter)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec moved on branch-identity refusal: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestArchiveRejectsHistoryChangeEvenWhenTreeMatchesReview(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	reviewedTree := orch.sess.Review.Fingerprint
	if err := os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("must not enter archive history\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "secret.txt")
	gitRun(t, dir, "commit", "-m", "unreviewed history")
	if err := os.Remove(filepath.Join(dir, "secret.txt")); err != nil {
		t.Fatal(err)
	}
	currentTree, err := orch.worktreeFingerprint(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if currentTree != reviewedTree {
		t.Fatalf("test setup tree = %s, want reviewed tree %s", currentTree, reviewedTree)
	}
	statusBefore := gitRun(t, dir, "status", "--porcelain=v1", "-z")
	indexBefore, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}

	err = orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "Git ref or HEAD changed after review") {
		t.Fatalf("Archive error = %v, want reviewed-history refusal", err)
	}
	indexAfter, err := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("history-identity refusal modified the Git index")
	}
	if statusAfter := gitRun(t, dir, "status", "--porcelain=v1", "-z"); statusAfter != statusBefore {
		t.Fatalf("history-identity refusal changed checkout:\nbefore %q\nafter  %q", statusBefore, statusAfter)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec moved on history-identity refusal: %v", err)
	}
}

func TestBuildAndReviewRejectBranchDifferentFromAcceptedWorkspace(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Add a workspace-bound helper"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "branch", "peer")
	gitRun(t, dir, "switch", "peer")
	if err := orch.Build(t.Context(), BuildOptions{}); err == nil || !strings.Contains(err.Error(), "active branch") {
		t.Fatalf("Build error = %v, want accepted-workspace refusal", err)
	}
	if orch.Phase() != session.PhaseSpec {
		t.Fatalf("phase after Build refusal = %q, want spec", orch.Phase())
	}

	gitRun(t, dir, "switch", "main")
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "switch", "peer")
	if _, err := orch.Review(t.Context()); err == nil || !strings.Contains(err.Error(), "active branch") {
		t.Fatalf("Review error = %v, want accepted-workspace refusal", err)
	}
	if orch.Phase() != session.PhaseBuild {
		t.Fatalf("phase after Review refusal = %q, want build", orch.Phase())
	}
}

func TestArchiveRejectsSymbolicHEADChangeAfterRefCommit(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	from := orch.Phase()
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	gitRun(t, dir, "branch", "peer", headBefore)
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}

	err = orch.publishArchiveTransactionWithGates(t.Context(), tx, nil, func() error {
		gitRun(t, dir, "symbolic-ref", "HEAD", "refs/heads/peer")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "workspace identity changed") {
		t.Fatalf("publish error = %v, want post-commit symbolic-HEAD refusal", err)
	}
	if tx.published {
		t.Fatal("symbolic-HEAD race left archive ref published")
	}
	if err := orch.rollbackArchive(t.Context(), from); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "main")); got != headBefore {
		t.Fatalf("main moved from %s to %s", headBefore, got)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); got != "peer" {
		t.Fatalf("concurrent symbolic HEAD change was overwritten: current %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec was not restored: %v", err)
	}
}

func TestArchiveRecoveryRestoresReviewBeforeSpecMove(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	notice, err := orch.recoverInterruptedArchive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "restored REVIEW") || orch.Phase() != session.PhaseReview {
		t.Fatalf("recovery = phase %q notice %q", orch.Phase(), notice)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("pre-move recovery changed HEAD from %s to %s", headBefore, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("pre-move recovery lost active spec: %v", err)
	}
}

func TestArchiveRecoveryRestoresSpecMovedBeforeCommit(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	notice, err := orch.recoverInterruptedArchive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "before commit publication") || orch.Phase() != session.PhaseReview {
		t.Fatalf("recovery = phase %q notice %q", orch.Phase(), notice)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); got != headBefore {
		t.Fatalf("pre-commit recovery changed HEAD from %s to %s", headBefore, got)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("pre-commit recovery did not restore active spec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "archive", specID)); !os.IsNotExist(err) {
		t.Fatalf("pre-commit recovery retained archive destination: %v", err)
	}
}

func TestArchiveRecoveryDoesNotRollbackOnDifferentUnpublishedBranch(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Add an identity-safe recovery helper"); err != nil {
		t.Fatal(err)
	}
	const featureBranch = "feat-recovery-identity"
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "branch", Name: featureBranch}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	specID := orch.spec.ID
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	// A same-OID symbolic switch does not touch the dirty worktree/index, but
	// recovery must not restore the moved folder under a different branch.
	gitRun(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")

	_, err := orch.recoverInterruptedArchive(t.Context())
	if err == nil || !strings.Contains(err.Error(), "publication is unproven") || !strings.Contains(err.Error(), "differs from the reviewed workspace") {
		t.Fatalf("recovery error = %v, want unpublished identity refusal", err)
	}
	if orch.Phase() != session.PhaseArchive {
		t.Fatalf("phase = %q, want archive", orch.Phase())
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "archive", specID, "spec.md")); err != nil {
		t.Fatalf("identity refusal moved archived spec: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID)); !os.IsNotExist(err) {
		t.Fatalf("identity refusal restored active spec: %v", err)
	}
}

func TestArchiveRecoveryRepairsIndexAfterPublishedRef(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveGitOutput(t.Context(), tx, nil, nil, "update-ref", tx.ref, tx.newCommit, tx.oldHead); err != nil {
		t.Fatal(err)
	}
	// Simulated crash point: HEAD contains the immutable reviewed archive tree,
	// while the real index still describes the old parent.
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); got != tx.newCommit {
		t.Fatalf("simulated published HEAD = %s, want %s", got, tx.newCommit)
	}

	notice, err := orch.recoverInterruptedArchive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "committed and recovered") || orch.Phase() != session.PhaseChat {
		t.Fatalf("recovery = phase %q notice %q", orch.Phase(), notice)
	}
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("published-ref recovery did not repair index cleanly: %q", status)
	}
	gitRun(t, dir, "cat-file", "-e", "HEAD:specs/archive/"+specID+"/spec.md")
	if orch.sess.SpecID != "" || orch.sess.Review != nil || orch.sess.SpecContract != nil || orch.sess.WorkspaceRef != "refs/heads/main" || filepathKey(orch.sess.Worktree) != filepathKey(dir) {
		t.Fatalf("published-ref recovery retained lifecycle state: %+v", orch.sess)
	}
}

func TestArchiveRecoveryFindsPublishedCommitAfterMergeSwitch(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Add a merge-recovery helper"); err != nil {
		t.Fatal(err)
	}
	const featureBranch = "feat-archive-recovery"
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "branch", Name: featureBranch}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	specID := orch.spec.ID
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	if err := orch.publishArchiveTransaction(t.Context(), tx); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash after mergeBranch switched back to the recorded base,
	// but before it merged the immutable archive commit or cleared the session.
	gitRun(t, dir, "switch", "main")
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("simulated merge target is dirty: %q", status)
	}

	notice, err := orch.recoverInterruptedArchive(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(notice, "committed and recovered") || orch.Phase() != session.PhaseChat {
		t.Fatalf("recovery = phase %q notice %q", orch.Phase(), notice)
	}
	if got := strings.TrimSpace(gitRun(t, dir, "branch", "--show-current")); got != "main" {
		t.Fatalf("recovery switched merge target from %q", got)
	}
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("merge-switch recovery changed the checkout: %q", status)
	}
	gitRun(t, dir, "cat-file", "-e", featureBranch+":specs/archive/"+specID+"/spec.md")
	if orch.sess.SpecID != "" || orch.sess.Review != nil || orch.sess.WorkspaceRef != "refs/heads/main" || filepathKey(orch.sess.Worktree) != filepathKey(dir) {
		t.Fatalf("merge-switch recovery retained lifecycle state: %+v", orch.sess)
	}
}

func TestArchiveRecoveryRunsOnSessionReload(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := t.TempDir()
	t.Setenv("MAESTRO_MEMORY_DIR", filepath.Join(t.TempDir(), "mem"))
	t.Setenv("MAESTRO_CHECKPOINTS_DIR", filepath.Join(t.TempDir(), "cps"))
	newOrch := func() *Orchestrator {
		t.Helper()
		var out bytes.Buffer
		runner := &fakeRunner{}
		orch, err := New(t.Context(), Options{
			ProjectDir:  dir,
			SessionsDir: sessionsDir,
			In:          strings.NewReader("n\n"),
			Out:         &out,
			Runner:      runner,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		runner.Wd = func() string { return orch.WorkDirDisplay() }
		return orch
	}
	orch := newOrch()
	if _, err := orch.Propose(t.Context(), "Add a reload-safe helper"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "stay"}); err != nil {
		t.Fatal(err)
	}
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	specID := orch.spec.ID
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveGitOutput(t.Context(), tx, nil, nil, "update-ref", tx.ref, tx.newCommit, tx.oldHead); err != nil {
		t.Fatal(err)
	}

	restored := newOrch()
	if restored.Phase() != session.PhaseChat || restored.ActiveSpec() != nil {
		t.Fatalf("reloaded recovery = phase %q spec %+v", restored.Phase(), restored.ActiveSpec())
	}
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("reloaded recovery left checkout dirty: %q", status)
	}
	gitRun(t, dir, "cat-file", "-e", "HEAD:specs/archive/"+specID+"/spec.md")
}

func TestArchiveRecoveryAfterMergedWorktreeWasRemoved(t *testing.T) {
	dir := newTestRepo(t)
	sessionsDir := t.TempDir()
	t.Setenv("MAESTRO_MEMORY_DIR", filepath.Join(t.TempDir(), "mem"))
	t.Setenv("MAESTRO_CHECKPOINTS_DIR", filepath.Join(t.TempDir(), "cps"))
	newOrch := func() *Orchestrator {
		t.Helper()
		var out bytes.Buffer
		runner := &fakeRunner{}
		orch, err := New(t.Context(), Options{
			ProjectDir:  dir,
			SessionsDir: sessionsDir,
			In:          strings.NewReader("n\n"),
			Out:         &out,
			Runner:      runner,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		runner.Wd = func() string { return orch.WorkDirDisplay() }
		return orch
	}

	orch := newOrch()
	if _, err := orch.Propose(t.Context(), "Add a removed-worktree recovery helper"); err != nil {
		t.Fatal(err)
	}
	const featureBranch = "feat-removed-worktree-recovery"
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "worktree", Name: featureBranch}); err != nil {
		t.Fatal(err)
	}
	worktree := orch.sess.Worktree
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	specID := orch.spec.ID
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	if err := orch.publishArchiveTransaction(t.Context(), tx); err != nil {
		t.Fatal(err)
	}

	// Simulate mergeBranch completing both the merge and managed-worktree
	// removal, followed by a crash before the lifecycle session is cleared.
	gitRun(t, dir, "merge", "--ff-only", featureBranch)
	gitRun(t, dir, "worktree", "remove", worktree)
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("simulated archive worktree still exists: %v", err)
	}

	restored := newOrch()
	if restored.Phase() != session.PhaseChat || restored.ActiveSpec() != nil {
		t.Fatalf("removed-worktree recovery = phase %q spec %+v", restored.Phase(), restored.ActiveSpec())
	}
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("removed-worktree recovery changed the base checkout: %q", status)
	}
	gitRun(t, dir, "cat-file", "-e", featureBranch+":specs/archive/"+specID+"/spec.md")
	gitRun(t, dir, "cat-file", "-e", "main:specs/archive/"+specID+"/spec.md")
}

func TestArchiveRecoveryPreservesUnexpectedStagedIndex(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveGitOutput(t.Context(), tx, nil, nil, "update-ref", tx.ref, tx.newCommit, tx.oldHead); err != nil {
		t.Fatal(err)
	}
	blob, err := runIsolatedGit(t.Context(), dir, strings.NewReader("concurrent staged data\n"), nil, "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runIsolatedGit(t.Context(), dir, nil, nil, "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(string(blob))+",concurrent-staged.txt"); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}

	_, err = orch.recoverInterruptedArchive(t.Context())
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite staged work") {
		t.Fatalf("recovery error = %v, want staged-index refusal", err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("archive recovery modified unexpected staged work")
	}
	if orch.Phase() != session.PhaseArchive {
		t.Fatalf("phase = %q, want archive for manual recovery", orch.Phase())
	}
}

func TestArchiveRecoveryRefusesExistingGitLockWithoutMutation(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	tx, err := orch.prepareArchiveTransaction(t.Context(), orch.commitMessage())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.cleanup()
	if err := orch.setPhase(session.PhaseArchive); err != nil {
		t.Fatal(err)
	}
	if err := orch.store.Archive(t.Context(), specID); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveGitOutput(t.Context(), tx, nil, nil, "update-ref", tx.ref, tx.newCommit, tx.oldHead); err != nil {
		t.Fatal(err)
	}
	indexBefore, err := os.ReadFile(tx.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	const lockMarker = "possible live Git owner\n"
	lockPath := tx.headPath + ".lock"
	if err := os.WriteFile(lockPath, []byte(lockMarker), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = orch.recoverInterruptedArchive(t.Context())
	if err == nil || !strings.Contains(err.Error(), "git lock") || !strings.Contains(err.Error(), "remove the stale lock") {
		t.Fatalf("recovery error = %v, want existing-lock refusal", err)
	}
	if orch.Phase() != session.PhaseArchive {
		t.Fatalf("phase = %q, want archive", orch.Phase())
	}
	if got := strings.TrimSpace(gitRun(t, dir, "rev-parse", tx.ref)); got != tx.newCommit {
		t.Fatalf("lock refusal moved reviewed ref to %s, want %s", got, tx.newCommit)
	}
	indexAfter, err := os.ReadFile(tx.indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(indexAfter, indexBefore) {
		t.Fatal("lock refusal modified the Git index")
	}
	if got, err := os.ReadFile(lockPath); err != nil || string(got) != lockMarker {
		t.Fatalf("lock refusal modified the existing lock: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "archive", specID, "spec.md")); err != nil {
		t.Fatalf("lock refusal moved the archived spec: %v", err)
	}
}

func TestArchiveRejectsSparseCheckoutWithoutMutation(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	gitRun(t, dir, "sparse-checkout", "init", "--cone")
	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD"))

	err = orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "sparse checkouts are not supported") {
		t.Fatalf("Archive error = %v, want sparse-checkout refusal", err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("sparse-checkout refusal modified the Git index")
	}
	if headAfter := strings.TrimSpace(gitRun(t, dir, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("sparse-checkout refusal moved HEAD from %s to %s", headBefore, headAfter)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("sparse-checkout refusal moved active spec: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestArchiveMergeBypassesMergeAndCheckoutHooks(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	if _, err := orch.Propose(t.Context(), "Add a hook-safe health endpoint"); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Accept(t.Context(), BranchChoice{Kind: "branch", Name: "feat-hook-safe"}); err != nil {
		t.Fatal(err)
	}

	baseWorktree := filepath.Join(t.TempDir(), "base")
	gitRun(t, dir, "worktree", "add", baseWorktree, "main")
	if err := os.WriteFile(filepath.Join(baseWorktree, "base.txt"), []byte("base advanced\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, baseWorktree, "add", "base.txt")
	gitRun(t, baseWorktree, "commit", "-m", "advance base")
	gitRun(t, dir, "worktree", "remove", baseWorktree)
	if err := orch.Build(t.Context(), BuildOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}

	hooksDir := filepath.Join(dir, ".git", "hooks")
	malicious := "#!/bin/sh\nprintf ran > merge-hook-ran\nprintf 'package main\\n' > merge_hook_payload.go\ngit add merge_hook_payload.go\n"
	for _, hook := range []string{"pre-merge-commit", "post-checkout", "post-merge"} {
		if err := os.WriteFile(filepath.Join(hooksDir, hook), []byte(malicious), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true, Merge: true}); err != nil {
		t.Fatalf("Archive --merge with malicious hooks: %v", err)
	}
	for _, name := range []string{"merge-hook-ran", "merge_hook_payload.go"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("disabled merge hook created %s: %v", name, err)
		}
	}
	if status := gitRun(t, dir, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("hook-safe merge left checkout dirty: %q", status)
	}
}

func TestArchiveRejectsCodeEditAfterReviewWithoutMutatingCheckout(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# externally edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "README.md")

	indexPath := filepath.Join(dir, ".git", "index")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	statusBefore := gitRun(t, dir, "status", "--porcelain=v1", "-z")
	contentBefore, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}

	err = orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "worktree changed after review") || !strings.Contains(err.Error(), "rerun /review") {
		t.Fatalf("Archive error = %v, want stale-review refusal", err)
	}
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	contentAfter, err := os.ReadFile(readme)
	if err != nil {
		t.Fatal(err)
	}
	if string(indexAfter) != string(indexBefore) {
		t.Fatal("archive refusal modified the Git index")
	}
	if string(contentAfter) != string(contentBefore) {
		t.Fatal("archive refusal modified the worktree")
	}
	if statusAfter := gitRun(t, dir, "status", "--porcelain=v1", "-z"); statusAfter != statusBefore {
		t.Fatalf("status changed on refusal:\nbefore %q\nafter  %q", statusBefore, statusAfter)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec moved on refusal: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", "archive", specID)); !os.IsNotExist(err) {
		t.Fatalf("archive destination exists after refusal: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestArchiveRechecksFingerprintAfterConfirmation(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	read := false
	orch.in = readerFunc(func(p []byte) (int, error) {
		if read {
			return 0, io.EOF
		}
		read = true
		if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# edited during confirmation\n"), 0o644); err != nil {
			t.Fatalf("edit during confirmation: %v", err)
		}
		return copy(p, "y\n"), nil
	})

	err := orch.Archive(t.Context(), ArchiveOptions{})
	if err == nil || !strings.Contains(err.Error(), "worktree changed after review") {
		t.Fatalf("Archive error = %v, want post-confirmation integrity refusal", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec moved on refusal: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestCompleteDocsAuthorizesOnlyAcceptedADR(t *testing.T) {
	orch, dir, _ := newReviewedPipeline(t)
	path, content, err := orch.DocsDraft(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# concurrent edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = orch.CompleteDocs(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "files other than the accepted ADR changed") || !strings.Contains(err.Error(), "rerun /review") {
		t.Fatalf("CompleteDocs error = %v, want unrelated-edit refusal", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestCompleteDocsRejectsSemanticallyInvalidADRWithoutRebaseline(t *testing.T) {
	orch, _, _ := newReviewedPipeline(t)
	path, content, err := orch.DocsDraft(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	content = strings.Replace(content, "\n## Alternatives", "\n- Deploy with TLS.\n\n## Alternatives", 1)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fingerprintBefore := orch.sess.Review.Fingerprint

	err = orch.CompleteDocs(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "accepted ADR violates the spec contract") || !strings.Contains(err.Error(), "introduced or altered a normative claim") {
		t.Fatalf("CompleteDocs error = %v, want semantic-contract refusal", err)
	}
	if orch.sess.Review.Fingerprint != fingerprintBefore {
		t.Fatalf("fingerprint changed from %q to %q", fingerprintBefore, orch.sess.Review.Fingerprint)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
	if got, readErr := os.ReadFile(path); readErr != nil || string(got) != content {
		t.Fatalf("semantic refusal changed ADR: %q, %v", got, readErr)
	}
}

func TestCompleteDocsRejectsSourcePathWithoutRebaseline(t *testing.T) {
	orch, dir, _ := newReviewedPipeline(t)
	fingerprintBefore := orch.sess.Review.Fingerprint
	mainPath := filepath.Join(dir, "main.go")
	modified := []byte("package main\n\nfunc main() { println(\"edited\") }\n")
	if err := os.WriteFile(mainPath, modified, 0o644); err != nil {
		t.Fatal(err)
	}

	err := orch.CompleteDocs(t.Context(), mainPath)
	if err == nil || !strings.Contains(err.Error(), "direct file in docs-archive/adr") {
		t.Fatalf("CompleteDocs error = %v, want arbitrary-source-path refusal", err)
	}
	if orch.sess.Review.Fingerprint != fingerprintBefore {
		t.Fatalf("fingerprint changed from %q to %q", fingerprintBefore, orch.sess.Review.Fingerprint)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
	if got, err := os.ReadFile(mainPath); err != nil || string(got) != string(modified) {
		t.Fatalf("source changed during refusal: %q, %v", got, err)
	}
}

func TestCompleteDocsRejectsSymlinkADRPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform privileges on Windows")
	}
	orch, _, specID := newReviewedPipeline(t)
	dir := orch.WorkDirDisplay()
	fingerprintBefore := orch.sess.Review.Fingerprint
	adrDir := filepath.Join(dir, "docs-archive", "adr")
	if err := os.MkdirAll(adrDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(adrDir, "2026-08-08-"+specID+".md")
	if err := os.Symlink(filepath.Join("..", "..", "main.go"), path); err != nil {
		t.Fatal(err)
	}

	err := orch.CompleteDocs(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("CompleteDocs error = %v, want symlink refusal", err)
	}
	if orch.sess.Review.Fingerprint != fingerprintBefore {
		t.Fatalf("fingerprint changed from %q to %q", fingerprintBefore, orch.sess.Review.Fingerprint)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestDocsRejectsSymlinkDirectoryBeforeWriting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires platform privileges on Windows")
	}
	orch, dir, _ := newReviewedPipeline(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "docs-archive")); err != nil {
		t.Fatal(err)
	}
	// Re-review the intentionally present repository state so DocsDraft reaches
	// the writer; the writer itself must reject before creating anything.
	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatal(err)
	}
	fingerprintBefore := orch.sess.Review.Fingerprint

	err := orch.Docs(t.Context())
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("Docs error = %v, want pre-write symlink refusal", err)
	}
	entries, readErr := os.ReadDir(outside)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("Docs wrote outside the worktree before refusal: %v", entries)
	}
	if orch.sess.Review.Fingerprint != fingerprintBefore {
		t.Fatal("Docs refusal rebaselined the review fingerprint")
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}

func TestArchiveRejectsEditedADRUntilReviewFromDocsRebaselines(t *testing.T) {
	orch, dir, _ := newReviewedPipeline(t)
	if err := orch.Docs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if orch.Phase() != session.PhaseDocs {
		t.Fatalf("phase = %q, want docs", orch.Phase())
	}
	paths, err := filepath.Glob(filepath.Join(dir, "docs-archive", "adr", "*.md"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("ADR paths = %v, %v", paths, err)
	}
	file, err := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\nManual clarification.\n"); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	err = orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "worktree changed after review") {
		t.Fatalf("Archive error = %v, want edited-ADR refusal", err)
	}
	if orch.Phase() != session.PhaseDocs {
		t.Fatalf("phase after refusal = %q, want docs", orch.Phase())
	}

	if _, err := orch.Review(t.Context()); err != nil {
		t.Fatalf("Review from docs: %v", err)
	}
	if orch.Phase() != session.PhaseDocs {
		t.Fatalf("phase after docs re-review = %q, want docs", orch.Phase())
	}
	if err := orch.Archive(t.Context(), ArchiveOptions{Yes: true}); err != nil {
		t.Fatalf("Archive after docs re-review: %v", err)
	}
}

func TestArchiveRejectsLegacyReviewWithoutFingerprint(t *testing.T) {
	orch, dir, specID := newReviewedPipeline(t)
	orch.sess.Review.Fingerprint = ""
	if err := orch.save(); err != nil {
		t.Fatal(err)
	}

	err := orch.Archive(t.Context(), ArchiveOptions{Yes: true})
	if err == nil || !strings.Contains(err.Error(), "no worktree fingerprint") || !strings.Contains(err.Error(), "rerun /review") {
		t.Fatalf("Archive error = %v, want legacy-review refusal", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "specs", specID, "spec.md")); err != nil {
		t.Fatalf("active spec moved on refusal: %v", err)
	}
	if orch.Phase() != session.PhaseReview {
		t.Fatalf("phase = %q, want review", orch.Phase())
	}
}
