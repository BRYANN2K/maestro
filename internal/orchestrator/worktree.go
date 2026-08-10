package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/git"
)

// isolatedRunner wraps a runner so its work happens in a dedicated detached
// git worktree. The runner starts from the active checkout's complete
// Git-visible filesystem state, including changes from the spec and previous
// build rounds. On success, only the runner's delta is applied back to the
// active checkout; no commit or merge happens before review.
type isolatedRunner struct {
	inner Runner
	o     *Orchestrator
}

// Run creates a detached worktree, runs the inner runner there, and applies
// its filesystem delta to the active checkout. Synthetic Git trees preserve
// binary files, executable bits, symlinks, deletions, and untracked files
// without modifying the user's index or advancing HEAD.
func (r *isolatedRunner) Run(ctx context.Context, role agentcore.Role, taskPrompt string) (res agentcore.AgentResult, runErr error) {
	activeWorkspace := r.o.workspaceRoute()
	activeDir := activeWorkspace.dir
	activeGit := activeWorkspace.git
	if activeDir == "" || activeGit == nil {
		return agentcore.AgentResult{}, errors.New("isolated run: active checkout is unavailable")
	}

	tempRoot, err := os.MkdirTemp("", "maestro-isolated-"+string(role)+"-")
	if err != nil {
		return agentcore.AgentResult{}, fmt.Errorf("isolated run: create temporary directory: %w", err)
	}
	wtPath := filepath.Join(tempRoot, "worktree")
	worktreeAdded := false
	defer func() {
		cleanupErr := cleanupIsolatedWorktree(activeDir, tempRoot, wtPath, worktreeAdded)
		if cleanupErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("isolated run cleanup: %w", cleanupErr))
		}
	}()

	baselineTree, err := snapshotWorktree(ctx, activeDir, filepath.Join(tempRoot, "baseline.index"), "HEAD")
	if err != nil {
		return agentcore.AgentResult{}, fmt.Errorf("isolated run snapshot: %w", err)
	}
	if _, err := runIsolatedGit(ctx, activeDir, nil, nil, "worktree", "add", "--detach", wtPath, "HEAD"); err != nil {
		return agentcore.AgentResult{}, fmt.Errorf("isolated run worktree: %w", err)
	}
	worktreeAdded = true
	if _, err := runIsolatedGit(ctx, wtPath, nil, nil, "read-tree", "--reset", "-u", baselineTree); err != nil {
		return agentcore.AgentResult{}, fmt.Errorf("isolated run populate: %w", err)
	}

	// Point native and injected runners at the isolated workspace. Always
	// restore the session checkout, including on errors and panics.
	r.o.installWorkspace(wtPath, git.New(wtPath), activeWorkspace.store)
	func() {
		defer func() {
			r.o.installWorkspace(activeWorkspace.dir, activeWorkspace.git, activeWorkspace.store)
		}()
		res, runErr = r.inner.Run(ctx, role, taskPrompt)
	}()
	if runErr != nil || !res.OK {
		return res, runErr
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}

	finalTree, err := snapshotWorktree(ctx, wtPath, filepath.Join(tempRoot, "final.index"), baselineTree)
	if err != nil {
		return res, fmt.Errorf("isolated run final snapshot: %w", err)
	}
	if finalTree == baselineTree {
		return res, nil
	}

	// Refuse to apply over concurrent edits. git apply is atomic by default,
	// but this equality check also prevents a valid fuzzy patch from silently
	// landing on a checkout that changed while the isolated runner was active.
	currentTree, err := snapshotWorktree(ctx, activeDir, filepath.Join(tempRoot, "current.index"), "HEAD")
	if err != nil {
		return res, fmt.Errorf("isolated run verify active checkout: %w", err)
	}
	if currentTree != baselineTree {
		return res, errors.New("isolated run: active checkout changed during the run; isolated changes were not applied")
	}

	patchPath := filepath.Join(tempRoot, "delta.patch")
	if err := writeTreeDiff(ctx, activeDir, baselineTree, finalTree, patchPath); err != nil {
		return res, fmt.Errorf("isolated run diff: %w", err)
	}
	patch, err := os.Open(patchPath)
	if err != nil {
		return res, fmt.Errorf("isolated run open delta: %w", err)
	}
	_, applyErr := runIsolatedGit(ctx, activeDir, patch, nil, "apply", "--binary", "--whitespace=nowarn")
	closeErr := patch.Close()
	if applyErr != nil || closeErr != nil {
		return res, fmt.Errorf("isolated run apply: %w", errors.Join(applyErr, closeErr))
	}
	return res, nil
}

// snapshotWorktree writes the Git-visible state of dir to a tree object using
// a private index. Existing staged state is never changed. Seeding the index
// from seed makes deletions observable; git add then overlays the filesystem.
func snapshotWorktree(ctx context.Context, dir, indexPath, seed string) (string, error) {
	env := map[string]string{"GIT_INDEX_FILE": indexPath}
	if _, err := runIsolatedGit(ctx, dir, nil, env, "read-tree", seed); err != nil {
		return "", err
	}
	if _, err := runIsolatedGit(ctx, dir, nil, env, "add", "-A", "--", "."); err != nil {
		return "", err
	}
	out, err := runIsolatedGit(ctx, dir, nil, env, "write-tree")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(string(out))
	if tree == "" {
		return "", errors.New("git write-tree returned an empty object ID")
	}
	return tree, nil
}

func writeTreeDiff(ctx context.Context, dir, before, after, path string) error {
	patch, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// Stream directly to disk rather than retaining an arbitrarily large
	// binary patch in memory.
	diffErr := streamIsolatedGit(ctx, dir, patch, "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", before, after, "--")
	return errors.Join(diffErr, patch.Close())
}

func cleanupIsolatedWorktree(activeDir, tempRoot, wtPath string, registered bool) error {
	// Cleanup must outlive a cancelled build context. The force applies only
	// to the unique temporary path created above, never to the active checkout.
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var errs []error
	worktreeRemoved := !registered
	if registered {
		if _, err := runIsolatedGit(cleanupCtx, activeDir, nil, nil, "worktree", "remove", "--force", wtPath); err != nil {
			errs = append(errs, err)
		} else {
			worktreeRemoved = true
		}
	}
	if _, err := runIsolatedGit(cleanupCtx, activeDir, nil, nil, "worktree", "prune"); err != nil {
		errs = append(errs, err)
	}
	if filepath.Dir(wtPath) != tempRoot || !strings.HasPrefix(filepath.Base(tempRoot), "maestro-isolated-") {
		errs = append(errs, errors.New("refusing to remove an unexpected temporary path"))
	} else if worktreeRemoved {
		if err := os.RemoveAll(tempRoot); err != nil {
			errs = append(errs, err)
		}
	} else {
		// If Git could not unregister the worktree, leave its directory intact
		// for diagnosis instead of creating stale administrative metadata.
		errs = append(errs, fmt.Errorf("temporary worktree retained at %s", wtPath))
	}
	return errors.Join(errs...)
}

func runIsolatedGit(ctx context.Context, dir string, stdin io.Reader, extraEnv map[string]string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = stdin
	cmd.Env = mergedEnvironment(extraEnv)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return out, nil
}

func streamIsolatedGit(ctx context.Context, dir string, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), message)
	}
	return nil
}

func mergedEnvironment(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return os.Environ()
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		name, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[name]; !replaced {
			env = append(env, item)
		}
	}
	for name, value := range overrides {
		env = append(env, name+"="+value)
	}
	return env
}

// isolatedRunner wraps a native runner in a worktree.
func (o *Orchestrator) isolatedRunner(inner Runner) (Runner, error) {
	return &isolatedRunner{inner: inner, o: o}, nil
}
