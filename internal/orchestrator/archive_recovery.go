package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
	"github.com/bryann2k/maestro/internal/spec"
)

// recoverInterruptedArchive resolves the only durable in-flight marker used
// by Archive: PhaseArchive. Folder presence alone is not success. Recovery
// derives the exact reviewed+move tree, then verifies HEAD, index, and the
// complete worktree before finalizing. Anything earlier is rolled back to a
// reviewable active spec without discarding concurrent user edits.
func (o *Orchestrator) recoverInterruptedArchive(ctx context.Context) (string, error) {
	if o.sess.Phase != session.PhaseArchive {
		return "", nil
	}
	id := strings.TrimSpace(o.sess.SpecID)
	if id == "" {
		o.sess.Phase = session.PhaseChat
		if err := o.save(); err != nil {
			return "", fmt.Errorf("recover archive: persist completed phase: %w", err)
		}
		return "completed archive phase recovered", nil
	}
	if err := rejectArchiveRecoveryLocks(ctx, o.workDir()); err != nil {
		return "", fmt.Errorf("recover archive: %w", err)
	}
	active, archived, err := inspectArchiveLocations(o.store, id)
	if err != nil {
		return "", fmt.Errorf("recover archive: %w", err)
	}
	if active && archived {
		return "", fmt.Errorf("recover archive: both active and archived spec %q exist; refusing automatic recovery", id)
	}

	// Legacy/incomplete gates cannot prove publication. Moving the folder back
	// is lossless and leaves any Git history or user edits untouched; /review
	// will establish a fresh release boundary.
	if o.sess.Review == nil || strings.TrimSpace(o.sess.Review.Fingerprint) == "" || strings.TrimSpace(o.sess.Review.GitRef) == "" || strings.TrimSpace(o.sess.Review.GitHead) == "" {
		if active {
			if err := o.restoreInterruptedArchivePhase(); err != nil {
				return "", err
			}
			return fmt.Sprintf("archive of %s stopped before the spec move; restored REVIEW", id), nil
		}
		if !archived {
			return "", fmt.Errorf("recover archive: neither active nor archived spec %q exists", id)
		}
		if err := o.restoreInterruptedArchiveSpec(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("archive of %s had no complete review identity; restored the active spec for /review", id), nil
	}

	tempRoot, err := os.MkdirTemp("", "maestro-archive-recovery-")
	if err != nil {
		return "", fmt.Errorf("recover archive: create private index: %w", err)
	}
	defer os.RemoveAll(tempRoot)
	expected, err := buildArchivedTree(ctx, o.workDir(), filepath.Join(tempRoot, "expected.index"), o.sess.Review.Fingerprint, id)
	if err != nil {
		return "", fmt.Errorf("recover archive: derive reviewed archive tree: %w", err)
	}

	archiveCommit, published, err := findPublishedArchiveCommit(ctx, o.workDir(), o.sess.Review.GitRef, o.sess.Review.GitHead, expected)
	if err != nil {
		return "", fmt.Errorf("recover archive: inspect reviewed branch history: %w", err)
	}
	identity, err := readGitWorkspaceIdentity(ctx, o.workDir())
	if err != nil {
		return "", fmt.Errorf("recover archive: inspect Git identity: %w", err)
	}
	if !published {
		if identity.ref != o.sess.Review.GitRef || identity.head != o.sess.Review.GitHead {
			return "", errors.New("recover archive: commit publication is unproven and the current Git ref/HEAD differs from the reviewed workspace; switch back to the reviewed ref at its reviewed commit, then restart Maestro")
		}
		if active {
			if err := o.restoreInterruptedArchivePhase(); err != nil {
				return "", err
			}
			return fmt.Sprintf("archive of %s stopped before the spec move; restored REVIEW", id), nil
		}
		if !archived {
			return "", fmt.Errorf("recover archive: neither active nor archived spec %q exists", id)
		}
		if err := o.restoreInterruptedArchiveSpec(ctx); err != nil {
			return "", err
		}
		return fmt.Sprintf("archive of %s stopped before commit publication; restored the active spec for /review", id), nil
	}

	if identity.ref == o.sess.Review.GitRef && identity.head == archiveCommit {
		if active || !archived {
			return "", fmt.Errorf("recover archive: reviewed branch contains the archive commit but filesystem move is incomplete")
		}
		if err := verifyArchiveWorktree(ctx, o, expected); err != nil {
			return "", fmt.Errorf("recover archive: committed tree has concurrent worktree edits: %w", err)
		}
		if err := o.recoverArchiveIndex(ctx, identity, expected, o.sess.Review.GitHead, tempRoot); err != nil {
			return "", err
		}
	} else {
		// Archive may have crashed during/after --merge. The reviewed branch is
		// authoritative proof that the immutable archive commit exists. Never
		// rewrite the index of a different checkout; require it to be clean and
		// leave merge/cleanup as an explicit follow-up.
		status, statusErr := o.git.Status(ctx)
		if statusErr != nil {
			return "", fmt.Errorf("recover archive: inspect current checkout after archive publication: %w", statusErr)
		}
		if status.Dirty {
			return "", errors.New("recover archive: archive commit exists on the reviewed branch, but the current checkout is dirty; resolve or abort the merge before restarting Maestro")
		}
	}
	return o.finalizeRecoveredArchive(ctx, id)
}

// A process crash can leave Git's conventional lock files behind. They may
// also belong to a still-running Git process, so recovery must never delete or
// overwrite them on inference alone. Refusing before any folder/session/index
// mutation keeps the archive retryable and gives the operator an exact lock to
// investigate. Once a confirmed stale lock is removed, restart resumes the
// deterministic recovery path.
func rejectArchiveRecoveryLocks(ctx context.Context, workDir string) error {
	for _, name := range []string{"index", "HEAD"} {
		path, err := resolveGitAdminPath(ctx, workDir, name)
		if err != nil {
			return fmt.Errorf("resolve Git %s lock path: %w", name, err)
		}
		lockPath := path + ".lock"
		if _, err := os.Lstat(lockPath); err == nil {
			return fmt.Errorf("git lock %q exists; verify that no Git process is running, remove the stale lock, then restart Maestro", lockPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect Git lock %q: %w", lockPath, err)
		}
	}
	return nil
}

func findPublishedArchiveCommit(ctx context.Context, dir, ref, reviewedHead, expectedTree string) (string, bool, error) {
	refOutput, err := runIsolatedGit(ctx, dir, nil, nil, "rev-parse", ref)
	if err != nil {
		return "", false, err
	}
	refHead := strings.TrimSpace(string(refOutput))
	if refHead == reviewedHead {
		return "", false, nil
	}
	commitsOutput, err := runIsolatedGit(ctx, dir, nil, nil, "rev-list", "--first-parent", "--reverse", reviewedHead+".."+refHead)
	if err != nil {
		return "", false, err
	}
	commits := strings.Fields(string(commitsOutput))
	if len(commits) == 0 {
		return "", false, nil
	}
	candidate := commits[0]
	parentOutput, err := runIsolatedGit(ctx, dir, nil, nil, "rev-parse", candidate+"^")
	if err != nil || strings.TrimSpace(string(parentOutput)) != reviewedHead {
		return "", false, nil
	}
	tree, err := revisionTree(ctx, dir, candidate)
	if err != nil {
		return "", false, err
	}
	if tree != expectedTree {
		return "", false, nil
	}
	return candidate, true, nil
}

func inspectArchiveLocations(store *spec.Store, id string) (active, archived bool, err error) {
	root := filepath.Clean(store.Dir())
	activePath := filepath.Join(root, id)
	activeInfo, activeErr := os.Lstat(activePath)
	if activeErr == nil {
		if activeInfo.Mode()&os.ModeSymlink != 0 || !activeInfo.IsDir() {
			return false, false, fmt.Errorf("active spec %q must be a real directory", activePath)
		}
		active = true
	} else if !errors.Is(activeErr, os.ErrNotExist) {
		return false, false, activeErr
	}
	archiveRoot := filepath.Join(root, spec.ArchiveDir)
	archiveInfo, archiveErr := os.Lstat(archiveRoot)
	if errors.Is(archiveErr, os.ErrNotExist) {
		return active, false, nil
	}
	if archiveErr != nil {
		return false, false, archiveErr
	}
	if archiveInfo.Mode()&os.ModeSymlink != 0 || !archiveInfo.IsDir() {
		return false, false, fmt.Errorf("archive root %q must be a real directory", archiveRoot)
	}
	archivedPath := filepath.Join(archiveRoot, id)
	archivedInfo, archivedErr := os.Lstat(archivedPath)
	if archivedErr == nil {
		if archivedInfo.Mode()&os.ModeSymlink != 0 || !archivedInfo.IsDir() {
			return false, false, fmt.Errorf("archived spec %q must be a real directory", archivedPath)
		}
		archived = true
	} else if !errors.Is(archivedErr, os.ErrNotExist) {
		return false, false, archivedErr
	}
	return active, archived, nil
}

func (o *Orchestrator) restoreInterruptedArchiveSpec(ctx context.Context) error {
	if err := o.store.RestoreArchive(ctx, o.sess.SpecID); err != nil {
		return fmt.Errorf("recover archive: restore active spec: %w", err)
	}
	return o.restoreInterruptedArchivePhase()
}

func (o *Orchestrator) restoreInterruptedArchivePhase() error {
	o.sess.Phase = session.PhaseReview
	if err := o.save(); err != nil {
		return fmt.Errorf("recover archive: persist REVIEW phase: %w", err)
	}
	return nil
}

func revisionTree(ctx context.Context, dir, revision string) (string, error) {
	out, err := runIsolatedGit(ctx, dir, nil, nil, "rev-parse", revision+"^{tree}")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", errors.New("git returned an empty tree ID")
	}
	return tree, nil
}

func (o *Orchestrator) recoverArchiveIndex(ctx context.Context, identity gitWorkspaceIdentity, expected, reviewedHead, tempRoot string) error {
	indexPath, err := resolveGitAdminPath(ctx, o.workDir(), "index")
	if err != nil {
		return fmt.Errorf("recover archive: resolve Git index: %w", err)
	}
	indexBefore, indexMode, err := readRegularIndex(indexPath)
	if err != nil {
		return fmt.Errorf("recover archive: read Git index: %w", err)
	}
	indexTree, err := treeFromIndexBytes(ctx, o.workDir(), filepath.Join(tempRoot, "current.index"), indexBefore)
	if err != nil {
		return fmt.Errorf("recover archive: inspect Git index tree: %w", err)
	}
	if indexTree == expected {
		return nil
	}
	reviewedHeadTree, err := revisionTree(ctx, o.workDir(), reviewedHead)
	if err != nil {
		return fmt.Errorf("recover archive: inspect reviewed HEAD tree: %w", err)
	}
	if indexTree != reviewedHeadTree {
		return fmt.Errorf("recover archive: Git index tree %s is neither archived tree %s nor pre-archive tree %s; refusing to overwrite staged work", indexTree, expected, reviewedHeadTree)
	}

	preparedPath := filepath.Join(tempRoot, "recovered.index")
	if _, err := runIsolatedGit(ctx, o.workDir(), nil, map[string]string{"GIT_INDEX_FILE": preparedPath}, "read-tree", expected); err != nil {
		return fmt.Errorf("recover archive: prepare committed index: %w", err)
	}
	indexAfter, _, err := readRegularIndex(preparedPath)
	if err != nil {
		return fmt.Errorf("recover archive: read committed index: %w", err)
	}
	lockPath := indexPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, indexMode.Perm())
	if err != nil {
		return fmt.Errorf("recover archive: lock Git index: %w", err)
	}
	lockOwned := true
	defer func() {
		_ = lock.Close()
		if lockOwned {
			_ = os.Remove(lockPath)
		}
	}()
	if err := verifyIndexBytes(indexPath, indexBefore); err != nil {
		return fmt.Errorf("recover archive: Git index changed during recovery: %w", err)
	}
	if err := writeFull(lock, indexAfter); err != nil {
		return fmt.Errorf("recover archive: write recovered index: %w", err)
	}
	if err := lock.Sync(); err != nil {
		return fmt.Errorf("recover archive: sync recovered index: %w", err)
	}
	if err := lock.Close(); err != nil {
		return fmt.Errorf("recover archive: close recovered index: %w", err)
	}

	headPath, err := resolveGitAdminPath(ctx, o.workDir(), "HEAD")
	if err != nil {
		return fmt.Errorf("recover archive: resolve HEAD path: %w", err)
	}
	_, headMode, err := readRegularIndex(headPath)
	if err != nil {
		return fmt.Errorf("recover archive: inspect HEAD: %w", err)
	}
	headLockPath := headPath + ".lock"
	headLock, err := os.OpenFile(headLockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, headMode.Perm())
	if err != nil {
		return fmt.Errorf("recover archive: lock HEAD: %w", err)
	}
	if err := headLock.Close(); err != nil {
		_ = os.Remove(headLockPath)
		return fmt.Errorf("recover archive: close HEAD lock: %w", err)
	}
	headLockOwned := true
	defer func() {
		if headLockOwned {
			_ = os.Remove(headLockPath)
		}
	}()
	currentIdentity, err := readGitWorkspaceIdentity(ctx, o.workDir())
	if err != nil || currentIdentity != identity {
		return errors.New("recover archive: Git identity changed while repairing the index")
	}
	if err := verifyArchiveWorktree(ctx, o, expected); err != nil {
		return fmt.Errorf("recover archive: worktree changed while repairing the index: %w", err)
	}
	if err := os.Rename(lockPath, indexPath); err != nil {
		return fmt.Errorf("recover archive: install recovered index: %w", err)
	}
	lockOwned = false
	if err := verifyArchiveWorktree(ctx, o, expected); err != nil {
		return fmt.Errorf("recover archive: worktree changed after index recovery: %w", err)
	}
	if err := os.Remove(headLockPath); err != nil {
		return fmt.Errorf("recover archive: release HEAD lock: %w", err)
	}
	headLockOwned = false
	return nil
}

func treeFromIndexBytes(ctx context.Context, workDir, privatePath string, data []byte) (string, error) {
	if err := os.WriteFile(privatePath, data, 0o600); err != nil {
		return "", err
	}
	out, err := runIsolatedGit(ctx, workDir, nil, map[string]string{"GIT_INDEX_FILE": privatePath}, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", errors.New("git returned an empty index tree")
	}
	return tree, nil
}

func resolveGitAdminPath(ctx context.Context, workDir, name string) (string, error) {
	out, err := runIsolatedGit(ctx, workDir, nil, nil, "rev-parse", "--git-path", name)
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return "", fmt.Errorf("git returned an empty %s path", name)
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(workDir, path)
	}
	return filepath.Clean(path), nil
}

func (o *Orchestrator) finalizeRecoveredArchive(ctx context.Context, id string) (string, error) {
	branch := strings.TrimPrefix(o.sess.WorkspaceRef, "refs/heads/")
	worktree := o.sess.Worktree
	baseIdentity, identityErr := git.RepositoryIdentity(ctx, o.baseDir)
	if identityErr != nil {
		return "", fmt.Errorf("recover archive: identify base workspace: %w", identityErr)
	}
	baseBranch, branchErr := git.New(baseIdentity.Worktree).CurrentBranch(ctx)
	if branchErr != nil {
		return "", fmt.Errorf("recover archive: identify base branch: %w", branchErr)
	}
	o.sess.SpecID = ""
	o.sess.WorkspaceRef = "refs/heads/" + baseBranch
	o.sess.Branch = ""
	o.sess.BaseBranch = ""
	o.sess.Worktree = baseIdentity.Worktree
	o.sess.ManagedWorktree = false
	o.sess.Draft = nil
	o.sess.DraftPrompt = ""
	o.sess.DraftDesign = ""
	o.sess.DraftTasks = ""
	o.sess.Review = nil
	o.sess.SpecContract = nil
	o.spec = nil
	if err := o.setPhase(session.PhaseChat); err != nil {
		return "", fmt.Errorf("recover archive: finalize session: %w", err)
	}
	o.installWorkspace(o.baseDir, git.New(o.baseDir), spec.NewStore(o.specsDir))
	_ = o.refreshGuardrails()
	// Keep this helper lifecycle-complete even when recovery is invoked outside
	// New (tests and future repair commands). New's later rebuild is harmless.
	o.newEcosystem()
	notice := fmt.Sprintf("archive of %s was committed and recovered on branch %s", id, branch)
	if worktree != "" {
		notice += fmt.Sprintf("; worktree %s was retained for manual merge/cleanup", worktree)
	}
	return notice, nil
}
