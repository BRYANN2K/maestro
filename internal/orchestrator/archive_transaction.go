package orchestrator

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// archiveTransaction is a prepared, immutable archive commit. The commit
// tree is derived from the reviewed fingerprint rather than from the live
// checkout, so a concurrent editor can never be swept into the commit.
type archiveTransaction struct {
	workDir      string
	tempRoot     string
	hooksDir     string
	privateIndex string
	indexPath    string
	indexMode    os.FileMode
	indexBefore  []byte
	indexAfter   []byte
	headPath     string
	headMode     os.FileMode
	ref          string
	oldHead      string
	expectedTree string
	newCommit    string
	published    bool
}

func (tx *archiveTransaction) cleanup() {
	if tx == nil || tx.tempRoot == "" {
		return
	}
	_ = os.RemoveAll(tx.tempRoot)
}

// prepareArchiveTransaction constructs the exact post-archive tree and its
// commit object without touching the user's index, worktree, or branch ref.
// Unreachable Git objects may be created; they cannot affect the checkout and
// are pruned normally if the transaction is later refused.
func (o *Orchestrator) prepareArchiveTransaction(ctx context.Context, message string) (_ *archiveTransaction, resultErr error) {
	if o.spec == nil || o.sess.Review == nil || strings.TrimSpace(o.sess.Review.Fingerprint) == "" {
		return nil, errors.New("archive transaction requires an active reviewed spec")
	}

	tempRoot, err := os.MkdirTemp("", "maestro-archive-")
	if err != nil {
		return nil, fmt.Errorf("archive: create private transaction: %w", err)
	}
	tx := &archiveTransaction{
		workDir:      o.workDir(),
		tempRoot:     tempRoot,
		hooksDir:     filepath.Join(tempRoot, "empty-hooks"),
		privateIndex: filepath.Join(tempRoot, "index"),
	}
	defer func() {
		if resultErr != nil {
			tx.cleanup()
		}
	}()
	if err := os.Mkdir(tx.hooksDir, 0o700); err != nil {
		return nil, fmt.Errorf("archive: create disabled hooks directory: %w", err)
	}
	rootOutput, err := runIsolatedGit(ctx, tx.workDir, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("archive: resolve repository root: %w", err)
	}
	root, err := canonicalExistingPath(strings.TrimSpace(string(rootOutput)))
	if err != nil {
		return nil, fmt.Errorf("archive: resolve repository root: %w", err)
	}
	workDir, err := canonicalExistingPath(tx.workDir)
	if err != nil {
		return nil, fmt.Errorf("archive: resolve active worktree root: %w", err)
	}
	if root != workDir {
		return nil, fmt.Errorf("archive: active workspace %q is not the repository root %q; restart Maestro from the repository root", workDir, root)
	}
	if err := rejectUnsupportedArchiveCheckout(ctx, tx.workDir); err != nil {
		return nil, err
	}

	indexOutput, err := runIsolatedGit(ctx, tx.workDir, nil, nil, "rev-parse", "--git-path", "index")
	if err != nil {
		return nil, fmt.Errorf("archive: resolve Git index: %w", err)
	}
	tx.indexPath = strings.TrimSpace(string(indexOutput))
	if tx.indexPath == "" {
		return nil, errors.New("archive: Git returned an empty index path")
	}
	if !filepath.IsAbs(tx.indexPath) {
		tx.indexPath = filepath.Join(tx.workDir, tx.indexPath)
	}
	tx.indexPath = filepath.Clean(tx.indexPath)
	tx.indexBefore, tx.indexMode, err = readRegularIndex(tx.indexPath)
	if err != nil {
		return nil, fmt.Errorf("archive: inspect Git index: %w", err)
	}
	headPathOutput, err := runIsolatedGit(ctx, tx.workDir, nil, nil, "rev-parse", "--git-path", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("archive: resolve Git HEAD path: %w", err)
	}
	tx.headPath = strings.TrimSpace(string(headPathOutput))
	if tx.headPath == "" {
		return nil, errors.New("archive: Git returned an empty HEAD path")
	}
	if !filepath.IsAbs(tx.headPath) {
		tx.headPath = filepath.Join(tx.workDir, tx.headPath)
	}
	tx.headPath = filepath.Clean(tx.headPath)
	_, tx.headMode, err = readRegularIndex(tx.headPath)
	if err != nil {
		return nil, fmt.Errorf("archive: inspect Git HEAD: %w", err)
	}
	headOutput, err := runIsolatedGit(ctx, tx.workDir, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("archive: resolve HEAD: %w", err)
	}
	tx.oldHead = strings.TrimSpace(string(headOutput))
	refOutput, err := runIsolatedGit(ctx, tx.workDir, nil, nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("archive: resolve current branch ref: %w", err)
	}
	tx.ref = strings.TrimSpace(string(refOutput))
	if !strings.HasPrefix(tx.ref, "refs/heads/") || strings.ContainsAny(tx.ref, "\r\n") {
		return nil, fmt.Errorf("archive: unsafe current branch ref %q", tx.ref)
	}
	if tx.ref != o.sess.WorkspaceRef || tx.ref != o.sess.Review.GitRef || tx.oldHead != o.sess.Review.GitHead {
		return nil, errors.New("archive: Git ref or HEAD no longer matches the reviewed workspace; rerun /review")
	}
	refHead, err := archiveGitOutput(ctx, tx, nil, nil, "rev-parse", tx.ref)
	if err != nil {
		return nil, fmt.Errorf("archive: resolve current branch: %w", err)
	}
	if strings.TrimSpace(string(refHead)) != tx.oldHead {
		return nil, errors.New("archive: HEAD moved while preparing the archive; retry /archive")
	}

	reviewed := strings.TrimSpace(o.sess.Review.Fingerprint)
	tx.expectedTree, err = buildArchivedTree(ctx, tx.workDir, tx.privateIndex, reviewed, o.spec.ID)
	if err != nil {
		return nil, err
	}
	if tx.expectedTree == "" || tx.expectedTree == reviewed {
		return nil, errors.New("archive: prepared tree did not record the spec move")
	}
	tx.indexAfter, _, err = readRegularIndex(tx.privateIndex)
	if err != nil {
		return nil, fmt.Errorf("archive: read prepared index: %w", err)
	}

	commitArgs := []string{"commit-tree", tx.expectedTree, "-p", tx.oldHead}
	signingOutput, err := archiveGitOutput(ctx, tx, nil, nil, "config", "--bool", "--default=false", "--get", "commit.gpgSign")
	if err != nil {
		return nil, fmt.Errorf("archive: inspect commit signing configuration: %w", err)
	}
	if strings.TrimSpace(string(signingOutput)) == "true" {
		commitArgs = append(commitArgs, "-S")
	}
	commitOutput, err := archiveGitOutput(ctx, tx, strings.NewReader(message+"\n"), nil, commitArgs...)
	if err != nil {
		return nil, fmt.Errorf("archive: create commit object: %w", err)
	}
	tx.newCommit = strings.TrimSpace(string(commitOutput))
	if tx.newCommit == "" {
		return nil, errors.New("archive: git commit-tree returned an empty object ID")
	}

	// This is the final read-only boundary before Archive changes phase and
	// moves the spec directory. It catches edits made while the private commit
	// was being assembled, and preserves the real index byte-for-byte on error.
	current, err := o.worktreeFingerprint(ctx)
	if err != nil {
		return nil, fmt.Errorf("archive: verify reviewed worktree before mutation: %w", err)
	}
	if current != reviewed {
		return nil, errors.New("archive: worktree changed while preparing the archive; rerun /review")
	}
	if err := verifyIndexBytes(tx.indexPath, tx.indexBefore); err != nil {
		return nil, fmt.Errorf("archive: Git index changed while preparing the archive: %w", err)
	}
	if err := verifyArchiveIdentity(ctx, tx, tx.oldHead); err != nil {
		return nil, fmt.Errorf("archive: workspace identity changed while preparing the archive: %w", err)
	}
	return tx, nil
}

func buildArchivedTree(ctx context.Context, workDir, indexPath, reviewed, specID string) (string, error) {
	env := map[string]string{"GIT_INDEX_FILE": indexPath}
	if _, err := runIsolatedGit(ctx, workDir, nil, env, "read-tree", reviewed); err != nil {
		return "", fmt.Errorf("archive: load reviewed tree: %w", err)
	}
	activePath := path.Join("specs", specID)
	archivePath := path.Join("specs", "archive", specID)
	subtreeOutput, err := runIsolatedGit(ctx, workDir, nil, env, "rev-parse", reviewed+":"+activePath)
	if err != nil {
		return "", fmt.Errorf("archive: reviewed tree has no active spec %q: %w", activePath, err)
	}
	subtree := strings.TrimSpace(string(subtreeOutput))
	typeOutput, err := runIsolatedGit(ctx, workDir, nil, env, "cat-file", "-t", subtree)
	if err != nil || strings.TrimSpace(string(typeOutput)) != "tree" {
		return "", fmt.Errorf("archive: reviewed spec %q is not a Git tree", activePath)
	}
	if _, err := runIsolatedGit(ctx, workDir, nil, env, "rm", "-r", "-f", "--cached", "--", activePath); err != nil {
		return "", fmt.Errorf("archive: remove active spec from prepared tree: %w", err)
	}
	if _, err := runIsolatedGit(ctx, workDir, nil, env, "read-tree", "--prefix="+archivePath+"/", subtree); err != nil {
		return "", fmt.Errorf("archive: add archived spec to prepared tree: %w", err)
	}
	treeOutput, err := runIsolatedGit(ctx, workDir, nil, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("archive: write prepared tree: %w", err)
	}
	tree := strings.TrimSpace(string(treeOutput))
	if tree == "" || tree == reviewed {
		return "", errors.New("archive: prepared tree did not record the spec move")
	}
	return tree, nil
}

// publishArchiveTransaction atomically advances the current branch to the
// prebuilt commit while installing its matching index. The caller has already
// moved the spec directory. A private tree comparison is performed both
// before and immediately after the ref CAS, so an autosave in the narrow
// pre-commit window is detected and the ref is rolled back.
func (o *Orchestrator) publishArchiveTransaction(ctx context.Context, tx *archiveTransaction) error {
	return o.publishArchiveTransactionWithGates(ctx, tx, nil, nil)
}

// publishArchiveTransactionWithGate exists to exercise the real TOCTOU
// boundary deterministically in tests. Production always passes a nil gate.
func (o *Orchestrator) publishArchiveTransactionWithGate(ctx context.Context, tx *archiveTransaction, beforeRefCAS func() error) (resultErr error) {
	return o.publishArchiveTransactionWithGates(ctx, tx, beforeRefCAS, nil)
}

func (o *Orchestrator) publishArchiveTransactionWithGates(ctx context.Context, tx *archiveTransaction, beforeRefCAS, afterRefCommit func() error) (resultErr error) {
	if tx == nil || tx.workDir != o.workDir() || tx.expectedTree == "" || tx.newCommit == "" {
		return errors.New("archive: invalid prepared transaction")
	}
	if err := verifyArchiveWorktree(ctx, o, tx.expectedTree); err != nil {
		return err
	}

	lockPath := tx.indexPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, tx.indexMode.Perm())
	if err != nil {
		return fmt.Errorf("archive: lock Git index without modifying it: %w", err)
	}
	lockOwned := true
	defer func() {
		if lock != nil {
			resultErr = errors.Join(resultErr, lock.Close())
		}
		if lockOwned {
			resultErr = errors.Join(resultErr, os.Remove(lockPath))
		}
	}()
	// Acquiring index.lock excludes cooperating Git writers. Compare the real
	// index only after the lock exists, then write the prepared bytes solely to
	// the lock file; the user's index remains untouched until the ref CAS wins.
	if err := verifyIndexBytes(tx.indexPath, tx.indexBefore); err != nil {
		return fmt.Errorf("archive: Git index changed during archive: %w", err)
	}
	if err := writeFull(lock, tx.indexAfter); err != nil {
		return fmt.Errorf("archive: prepare Git index lock: %w", err)
	}
	if err := lock.Sync(); err != nil {
		return fmt.Errorf("archive: sync prepared Git index: %w", err)
	}
	if err := lock.Close(); err != nil {
		lock = nil
		return fmt.Errorf("archive: close prepared Git index: %w", err)
	}
	lock = nil
	if err := verifyIndexBytes(lockPath, tx.indexAfter); err != nil {
		return fmt.Errorf("archive: verify prepared Git index: %w", err)
	}
	if err := verifyIndexBytes(tx.indexPath, tx.indexBefore); err != nil {
		return fmt.Errorf("archive: Git index changed during archive: %w", err)
	}
	if err := verifyArchiveIdentity(ctx, tx, tx.oldHead); err != nil {
		return fmt.Errorf("archive: workspace identity changed during archive: %w", err)
	}
	if err := verifyArchiveWorktree(ctx, o, tx.expectedTree); err != nil {
		return err
	}
	refUpdate, err := beginArchiveRefUpdate(ctx, tx)
	if err != nil {
		return err
	}
	refCommitted := false
	defer func() {
		if !refCommitted {
			resultErr = errors.Join(resultErr, refUpdate.abort())
		}
	}()
	// update-ref's prepared transaction now owns the checked-out ref locks.
	// A cooperating git switch/update cannot change symbolic HEAD until the
	// compare-and-swap commits or aborts.
	if err := verifyArchiveIdentity(ctx, tx, tx.oldHead); err != nil {
		return fmt.Errorf("archive: workspace identity changed before publication: %w", err)
	}
	if beforeRefCAS != nil {
		if err := beforeRefCAS(); err != nil {
			return fmt.Errorf("archive: before-ref gate: %w", err)
		}
	}

	if err := refUpdate.commit(); err != nil {
		tx.published = archiveRefEquals(tx, tx.newCommit)
		return fmt.Errorf("archive: publish commit with branch compare-and-swap: %w", err)
	}
	refCommitted = true
	tx.published = true
	if afterRefCommit != nil {
		if err := afterRefCommit(); err != nil {
			if rollbackErr := rollbackArchiveRef(tx); rollbackErr != nil {
				return errors.Join(fmt.Errorf("archive: after-ref gate: %w", err), fmt.Errorf("archive: commit was published but ref rollback failed: %w", rollbackErr))
			}
			return fmt.Errorf("archive: after-ref gate: %w", err)
		}
	}
	headLockPath := tx.headPath + ".lock"
	headLock, err := os.OpenFile(headLockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, tx.headMode.Perm())
	if err != nil {
		if rollbackErr := rollbackArchiveRef(tx); rollbackErr != nil {
			return errors.Join(fmt.Errorf("archive: lock HEAD after publication: %w", err), fmt.Errorf("archive: commit was published but ref rollback failed: %w", rollbackErr))
		}
		return fmt.Errorf("archive: lock HEAD after publication: %w", err)
	}
	if err := headLock.Close(); err != nil {
		_ = os.Remove(headLockPath)
		if rollbackErr := rollbackArchiveRef(tx); rollbackErr != nil {
			return errors.Join(fmt.Errorf("archive: close HEAD lock after publication: %w", err), fmt.Errorf("archive: commit was published but ref rollback failed: %w", rollbackErr))
		}
		return fmt.Errorf("archive: close HEAD lock after publication: %w", err)
	}
	releaseHEAD := func() error { return os.Remove(headLockPath) }
	if err := verifyArchiveIdentity(ctx, tx, tx.newCommit); err != nil {
		releaseErr := releaseHEAD()
		if rollbackErr := rollbackArchiveRef(tx); rollbackErr != nil {
			return errors.Join(fmt.Errorf("archive: workspace identity changed while publishing: %w", err), fmt.Errorf("archive: commit was published but ref rollback failed: %w", rollbackErr))
		}
		return errors.Join(fmt.Errorf("archive: workspace identity changed while publishing: %w", err), releaseErr)
	}

	// The tree is immutable and therefore already excludes concurrent edits.
	// This second snapshot additionally detects an autosave between the last
	// pre-CAS check and update-ref, allowing a clean ref rollback while the real
	// index still contains its original bytes.
	if err := verifyArchiveWorktree(ctx, o, tx.expectedTree); err != nil {
		releaseErr := releaseHEAD()
		if rollbackErr := rollbackArchiveRef(tx); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("archive: commit was published but ref rollback failed: %w", rollbackErr))
		}
		return errors.Join(err, releaseErr)
	}
	if err := os.Rename(lockPath, tx.indexPath); err != nil {
		releaseErr := releaseHEAD()
		if rollbackErr := rollbackArchiveRef(tx); rollbackErr != nil {
			return errors.Join(fmt.Errorf("archive: install prepared Git index: %w", err), fmt.Errorf("archive: commit was published but ref rollback failed: %w", rollbackErr))
		}
		return errors.Join(fmt.Errorf("archive: install prepared Git index: %w", err), releaseErr)
	}
	lockOwned = false
	if err := releaseHEAD(); err != nil {
		return fmt.Errorf("archive: release HEAD lock after publication: %w", err)
	}
	return nil
}

func verifyArchiveWorktree(ctx context.Context, o *Orchestrator, expected string) error {
	tempRoot, err := os.MkdirTemp("", "maestro-archive-verify-")
	if err != nil {
		return fmt.Errorf("archive: create verification index: %w", err)
	}
	defer os.RemoveAll(tempRoot)
	current, err := snapshotWorktree(ctx, o.workDir(), filepath.Join(tempRoot, "index"), expected)
	if err != nil {
		return fmt.Errorf("archive: verify post-archive worktree: %w", err)
	}
	if current != expected {
		return errors.New("archive: worktree changed during archive; no unreviewed changes were committed; rerun /review")
	}
	return nil
}

func rollbackArchiveRef(tx *archiveTransaction) error {
	if !tx.published {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := archiveGitOutput(ctx, tx, nil, nil, "update-ref", tx.ref, tx.oldHead, tx.newCommit); err != nil {
		return err
	}
	tx.published = false
	return nil
}

func verifyArchiveIdentity(ctx context.Context, tx *archiveTransaction, expectedHead string) error {
	refOutput, err := archiveGitOutput(ctx, tx, nil, nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve symbolic HEAD: %w", err)
	}
	if ref := strings.TrimSpace(string(refOutput)); ref != tx.ref {
		return fmt.Errorf("symbolic HEAD is %q, want %q", ref, tx.ref)
	}
	headOutput, err := archiveGitOutput(ctx, tx, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("resolve HEAD commit: %w", err)
	}
	if head := strings.TrimSpace(string(headOutput)); head != expectedHead {
		return fmt.Errorf("HEAD is %q, want %q", head, expectedHead)
	}
	return nil
}

type archiveRefUpdate struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	stdout   *bufio.Reader
	stderr   bytes.Buffer
	finished bool
}

func beginArchiveRefUpdate(ctx context.Context, tx *archiveTransaction) (_ *archiveRefUpdate, resultErr error) {
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath="+tx.hooksDir, "update-ref", "--stdin")
	cmd.Dir = tx.workDir
	cmd.Env = mergedEnvironment(nil)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("archive: open ref transaction input: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("archive: open ref transaction output: %w", err)
	}
	r := &archiveRefUpdate{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	cmd.Stderr = &r.stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("archive: start ref transaction: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, r.abort())
		}
	}()
	if err := r.command("start", "start: ok"); err != nil {
		return nil, fmt.Errorf("archive: begin ref transaction: %w", err)
	}
	if _, err := fmt.Fprintf(r.stdin, "update %s %s %s\n", tx.ref, tx.newCommit, tx.oldHead); err != nil {
		return nil, fmt.Errorf("archive: queue ref compare-and-swap: %w", err)
	}
	if err := r.command("prepare", "prepare: ok"); err != nil {
		return nil, fmt.Errorf("archive: prepare ref compare-and-swap: %w", err)
	}
	return r, nil
}

func (r *archiveRefUpdate) command(command, expected string) error {
	if r == nil || r.finished {
		return errors.New("ref transaction is not active")
	}
	if _, err := io.WriteString(r.stdin, command+"\n"); err != nil {
		return err
	}
	line, err := r.stdout.ReadString('\n')
	if err != nil {
		return errors.Join(err, errors.New(strings.TrimSpace(r.stderr.String())))
	}
	if strings.TrimSpace(line) != expected {
		return fmt.Errorf("unexpected git response %q", strings.TrimSpace(line))
	}
	return nil
}

func (r *archiveRefUpdate) commit() error {
	if err := r.command("commit", "commit: ok"); err != nil {
		return err
	}
	return r.finish()
}

func (r *archiveRefUpdate) abort() error {
	if r == nil || r.finished {
		return nil
	}
	commandErr := r.command("abort", "abort: ok")
	return errors.Join(commandErr, r.finish())
}

func (r *archiveRefUpdate) finish() error {
	if r == nil || r.finished {
		return nil
	}
	r.finished = true
	closeErr := r.stdin.Close()
	waitErr := r.cmd.Wait()
	if waitErr != nil && strings.TrimSpace(r.stderr.String()) != "" {
		waitErr = errors.New(strings.TrimSpace(r.stderr.String()))
	}
	return errors.Join(closeErr, waitErr)
}

func archiveRefEquals(tx *archiveTransaction, expected string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := archiveGitOutput(ctx, tx, nil, nil, "rev-parse", tx.ref)
	return err == nil && strings.TrimSpace(string(out)) == expected
}

func rejectUnsupportedArchiveCheckout(ctx context.Context, workDir string) error {
	sparseOutput, err := runIsolatedGit(ctx, workDir, nil, nil, "config", "--bool", "--default=false", "--get", "core.sparseCheckout")
	if err != nil {
		return fmt.Errorf("archive: inspect sparse-checkout configuration: %w", err)
	}
	if strings.TrimSpace(string(sparseOutput)) == "true" {
		return errors.New("archive: sparse checkouts are not supported by the release transaction; use a full checkout before /review and /archive")
	}
	flagsOutput, err := runIsolatedGit(ctx, workDir, nil, nil, "ls-files", "-v", "-z")
	if err != nil {
		return fmt.Errorf("archive: inspect Git index flags: %w", err)
	}
	for _, record := range bytes.Split(flagsOutput, []byte{0}) {
		if len(record) < 2 || record[1] != ' ' {
			continue
		}
		tag := record[0]
		if tag == 'S' || (tag >= 'a' && tag <= 'z') {
			return errors.New("archive: skip-worktree/assume-unchanged index flags are not supported; clear them before /review and /archive")
		}
	}
	return nil
}

func canonicalExistingPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func archiveGitOutput(ctx context.Context, tx *archiveTransaction, stdin io.Reader, env map[string]string, args ...string) ([]byte, error) {
	withDisabledHooks := make([]string, 0, len(args)+2)
	withDisabledHooks = append(withDisabledHooks, "-c", "core.hooksPath="+tx.hooksDir)
	withDisabledHooks = append(withDisabledHooks, args...)
	return runIsolatedGit(ctx, tx.workDir, stdin, env, withDisabledHooks...)
}

func readRegularIndex(path string) ([]byte, os.FileMode, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, fmt.Errorf("%q must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	return data, info.Mode(), nil
}

func verifyIndexBytes(path string, expected []byte) error {
	actual, _, err := readRegularIndex(path)
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return errors.New("contents no longer match the pre-archive index")
	}
	return nil
}

func writeFull(dst io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := dst.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
