package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/spec"
)

const (
	initialCommitMessage       = "chore: initialize project"
	initialCommitPathBatchSize = 128
	managedWorktreeAttempts    = 128
)

type acceptRepositorySetup struct {
	Initialized     bool
	BaselineCommit  string
	ExcludedPrivate int
}

// EnsureBootstrapRepository gives an explicit greenfield /bootstrap a local
// Git repository before project discovery begins. It intentionally creates no
// commit and stages no files; /accept remains responsible for establishing the
// safe committed baseline required by a linked worktree.
func (o *Orchestrator) EnsureBootstrapRepository(ctx context.Context) (bool, error) {
	initialized, err := ensureRepositoryInitialized(ctx, o.workspaceRoute())
	if err != nil {
		return false, fmt.Errorf("bootstrap: initialize Git repository: %w", err)
	}
	if initialized {
		o.RefreshBranchDisplay()
	}
	return initialized, nil
}

func ensureRepositoryInitialized(ctx context.Context, workspace workspaceRoute) (bool, error) {
	if workspace.git.IsRepo(ctx) {
		return false, nil
	}
	if _, err := runIsolatedGit(ctx, workspace.dir, nil, nil, "init", "--initial-branch=main"); err != nil {
		return false, err
	}
	return true, nil
}

// ensureAcceptRepository gives every accepted proposal a committed Git base.
// Existing repositories with a valid HEAD are left byte-for-byte alone. A
// plain directory (or an unborn repository) receives one baseline commit so a
// linked worktree can be created without asking the user to learn Git first.
func (o *Orchestrator) ensureAcceptRepository(ctx context.Context, workspace workspaceRoute) (acceptRepositorySetup, error) {
	var setup acceptRepositorySetup
	initialized, err := ensureRepositoryInitialized(ctx, workspace)
	if err != nil {
		return setup, fmt.Errorf("initialize repository: %w", err)
	}
	setup.Initialized = initialized
	if _, err := workspace.git.HeadOID(ctx); err == nil {
		return setup, nil
	}
	if _, err := workspace.git.CurrentBranch(ctx); err != nil {
		return setup, fmt.Errorf("identify initial branch: %w", err)
	}

	indexed, err := workspace.git.TrackedFiles(ctx)
	if err != nil {
		return setup, fmt.Errorf("inspect initial index: %w", err)
	}
	var sensitiveIndexed []string
	sensitive := make(map[string]struct{})
	statePrefix := initialCommitStatePrefix(workspace.dir, o.sessions.Dir())
	for _, path := range indexed {
		if excludedInitialCommitPath(path, statePrefix) {
			sensitiveIndexed = append(sensitiveIndexed, path)
			sensitive[path] = struct{}{}
		}
	}
	if err := runGitPathBatches(ctx, workspace.dir, []string{"rm", "--cached", "--ignore-unmatch", "--"}, sensitiveIndexed); err != nil {
		return setup, fmt.Errorf("exclude sensitive indexed paths: %w", err)
	}

	untracked, err := workspace.git.UntrackedFiles(ctx)
	if err != nil {
		return setup, fmt.Errorf("inspect initial files: %w", err)
	}
	safe := make([]string, 0, len(untracked))
	for _, path := range untracked {
		if excludedInitialCommitPath(path, statePrefix) {
			sensitive[path] = struct{}{}
			continue
		}
		safe = append(safe, path)
	}
	setup.ExcludedPrivate = len(sensitive)
	if err := runGitPathBatches(ctx, workspace.dir, []string{"add", "--"}, safe); err != nil {
		return setup, fmt.Errorf("stage initial project files: %w", err)
	}
	if err := ensureManagedGitIdentity(ctx, workspace.dir); err != nil {
		return setup, err
	}

	hooksDir, err := os.MkdirTemp("", "maestro-initial-hooks-")
	if err != nil {
		return setup, fmt.Errorf("create isolated hooks directory: %w", err)
	}
	defer os.RemoveAll(hooksDir)
	if err := os.Chmod(hooksDir, 0o700); err != nil {
		return setup, fmt.Errorf("protect isolated hooks directory: %w", err)
	}
	if _, err := runIsolatedGit(ctx, workspace.dir, nil, map[string]string{"GIT_TERMINAL_PROMPT": "0"},
		"-c", "core.hooksPath="+hooksDir,
		"-c", "commit.gpgSign=false",
		"commit", "--allow-empty", "-m", initialCommitMessage,
	); err != nil {
		return setup, fmt.Errorf("create baseline commit: %w", err)
	}
	setup.BaselineCommit, err = workspace.git.CommitHash(ctx)
	if err != nil {
		return setup, fmt.Errorf("resolve baseline commit: %w", err)
	}
	return setup, nil
}

func ensureManagedGitIdentity(ctx context.Context, dir string) error {
	for key, fallback := range map[string]string{
		"user.name":  "Maestro",
		"user.email": "maestro@localhost",
	} {
		out, err := runIsolatedGit(ctx, dir, nil, nil, "config", "--get", key)
		if err == nil && strings.TrimSpace(string(out)) != "" {
			continue
		}
		if _, err := runIsolatedGit(ctx, dir, nil, nil, "config", "--local", key, fallback); err != nil {
			return fmt.Errorf("configure managed Git identity %s: %w", key, err)
		}
	}
	return nil
}

func runGitPathBatches(ctx context.Context, dir string, prefix, paths []string) error {
	for start := 0; start < len(paths); start += initialCommitPathBatchSize {
		end := min(start+initialCommitPathBatchSize, len(paths))
		args := append(append([]string(nil), prefix...), paths[start:end]...)
		if _, err := runIsolatedGit(ctx, dir, nil, nil, args...); err != nil {
			return err
		}
	}
	return nil
}

func sensitiveInitialCommitPath(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	parts := strings.Split(lower, "/")
	for _, part := range parts {
		switch part {
		case ".ssh", ".aws", ".kube", ".gnupg", ".azure", ".gcloud", "credentials", "secrets", "secret", "vault":
			return true
		}
	}
	base := parts[len(parts)-1]
	if base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasPrefix(base, ".env-") {
		return true
	}
	switch base {
	case ".npmrc", ".pypirc", ".netrc", ".yarnrc", ".yarnrc.yml", "pip.conf", "credentials.json", "service-account.json":
		return true
	}
	for _, suffix := range []string{".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".tfstate", ".tfstate.backup"} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	return strings.Contains(base, "credential") || strings.Contains(base, "secret") || strings.Contains(base, "private-key")
}

func initialCommitStatePrefix(projectDir, stateDir string) string {
	if canonical, err := canonicalProjectDir(projectDir); err == nil {
		projectDir = canonical
	}
	if canonical, err := canonicalProjectDir(stateDir); err == nil {
		stateDir = canonical
	}
	relative, err := filepath.Rel(projectDir, stateDir)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(filepath.Clean(relative))
}

func excludedInitialCommitPath(path, statePrefix string) bool {
	if sensitiveInitialCommitPath(path) {
		return true
	}
	path = filepath.ToSlash(path)
	return statePrefix == "." || (statePrefix != "" && (path == statePrefix || strings.HasPrefix(path, statePrefix+"/")))
}

// createManagedAcceptWorktree allocates a collision-free branch and checkout
// below Maestro's private state directory. Existing branches and paths are
// preserved; retries receive a deterministic numeric suffix.
func (o *Orchestrator) createManagedAcceptWorktree(ctx context.Context, workspace workspaceRoute, draft *spec.Spec) (BranchChoice, string, string, error) {
	baseName := prefixFor(draft.Category) + draft.ID
	home, err := os.UserHomeDir()
	if err != nil {
		return BranchChoice{}, "", "", fmt.Errorf("accept: resolve home: %w", err)
	}
	parent, err := managedWorkspaceParent(home, o.sess.Project)
	if err != nil {
		return BranchChoice{}, "", "", fmt.Errorf("accept: prepare managed worktree directory: %w", err)
	}
	repository, err := git.RepositoryIdentity(ctx, workspace.dir)
	if err != nil {
		return BranchChoice{}, "", "", fmt.Errorf("accept: identify repository: %w", err)
	}
	if pathContains(repository.CommonDir, parent) || pathContains(parent, repository.CommonDir) {
		return BranchChoice{}, "", "", errors.New("accept: managed worktree directory overlaps the Git common directory")
	}
	startOID, err := workspace.git.HeadOID(ctx)
	if err != nil {
		return BranchChoice{}, "", "", fmt.Errorf("accept: determine worktree start: %w", err)
	}

	for attempt := 0; attempt < managedWorktreeAttempts; attempt++ {
		name := baseName
		pathSlug := workspaceSlug(baseName)
		if attempt > 0 {
			name = fmt.Sprintf("%s-%d", baseName, attempt+1)
			// Put the counter first so a long slug cannot truncate the unique
			// portion of the managed directory name.
			pathSlug = workspaceSlug(fmt.Sprintf("%d-%s", attempt+1, baseName))
		}
		path := filepath.Join(parent, pathSlug)
		if _, branchErr := workspace.git.BranchOID(ctx, name); branchErr == nil {
			continue
		}
		if _, pathErr := os.Lstat(path); pathErr == nil {
			continue
		} else if !errors.Is(pathErr, os.ErrNotExist) {
			return BranchChoice{}, "", "", fmt.Errorf("accept: inspect managed worktree target %q: %w", path, pathErr)
		}
		created, err := workspace.git.CreateLinkedWorktree(ctx, path, name, startOID)
		if err != nil {
			return BranchChoice{}, "", "", fmt.Errorf("accept: %w", err)
		}
		return BranchChoice{Kind: "worktree", Name: name}, created.Path, created.Head, nil
	}
	return BranchChoice{}, "", "", fmt.Errorf("accept: could not allocate a managed worktree name after %d attempts", managedWorktreeAttempts)
}
