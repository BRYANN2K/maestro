package git

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
	"sync"
	"time"
)

const (
	workspaceListOutputLimit = 8 << 20
	workspaceListMaxRecords  = 256
	workspaceScanTimeout     = 5 * time.Second
	workspaceScanParallel    = 4
)

// Repository identifies a checkout and the shared Git administration
// directory used by every linked worktree.
type Repository struct {
	Worktree  string
	CommonDir string
}

// Workspace is one exact record from git worktree list --porcelain -z.
// Ref is fully qualified when the worktree is attached to a local branch.
type Workspace struct {
	Path           string
	Head           string
	Ref            string
	Branch         string
	Current        bool
	Locked         bool
	LockReason     string
	Prunable       bool
	PrunableReason string
	Detached       bool
	Bare           bool
	Dirty          bool
	Healthy        bool
	DisabledReason string
}

// RepositoryIdentity returns canonical paths without trimming legal spaces or
// newlines from repository names.
func RepositoryIdentity(ctx context.Context, dir string) (Repository, error) {
	c := New(dir)
	worktreeOut, err := c.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("repository identity: %w", err)
	}
	worktreeRaw, err := gitOutputPath(worktreeOut)
	if err != nil {
		return Repository{}, fmt.Errorf("repository identity: worktree: %w", err)
	}
	worktree, err := canonicalPath(worktreeRaw)
	if err != nil {
		return Repository{}, fmt.Errorf("repository identity: worktree: %w", err)
	}
	commonOut, err := c.run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("repository identity: %w", err)
	}
	commonRaw, err := gitOutputPath(commonOut)
	if err != nil {
		return Repository{}, fmt.Errorf("repository identity: common dir: %w", err)
	}
	if !filepath.IsAbs(commonRaw) {
		commonRaw = filepath.Join(dir, commonRaw)
	}
	commonDir, err := canonicalPath(commonRaw)
	if err != nil {
		return Repository{}, fmt.Errorf("repository identity: common dir: %w", err)
	}
	return Repository{Worktree: worktree, CommonDir: commonDir}, nil
}

func gitOutputPath(out []byte) (string, error) {
	if len(out) == 0 || out[len(out)-1] != '\n' {
		return "", errors.New("git returned an unterminated path")
	}
	value := string(out[:len(out)-1])
	if value == "" || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("git returned an invalid path")
	}
	return value, nil
}

// ListWorkspaces returns all registered worktrees, including disabled rows.
// Porcelain -z is required so tabs, newlines, and spaces in paths are exact.
func (c *Client) ListWorkspaces(ctx context.Context) ([]Workspace, error) {
	repository, err := RepositoryIdentity(ctx, c.dir)
	if err != nil {
		return nil, err
	}
	out, err := c.runWorkspaceList(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	workspaces, err := parseWorktreePorcelainZ(out)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	if len(workspaces) > workspaceListMaxRecords {
		return nil, fmt.Errorf("list workspaces: %d records exceed the %d-record safety limit", len(workspaces), workspaceListMaxRecords)
	}
	type workspaceCheck struct {
		canonicalErr error
		statusErr    error
		identityErr  error
		commonDir    string
	}
	checks := make([]workspaceCheck, len(workspaces))
	for i := range workspaces {
		workspace := &workspaces[i]
		canonical, canonicalErr := canonicalPath(workspace.Path)
		if canonicalErr == nil {
			workspace.Path = canonical
			workspace.Current = canonical == repository.Worktree
		}
		checks[i].canonicalErr = canonicalErr
	}

	scanCtx, cancelScans := context.WithTimeout(ctx, workspaceScanTimeout)
	defer cancelScans()
	semaphore := make(chan struct{}, workspaceScanParallel)
	var wg sync.WaitGroup
	for i := range workspaces {
		workspace := &workspaces[i]
		if checks[i].canonicalErr != nil || workspace.Bare || workspace.Prunable || workspace.Locked || workspace.Detached || workspace.Ref == "" || !strings.HasPrefix(workspace.Ref, "refs/heads/") {
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-scanCtx.Done():
				checks[i].statusErr = scanCtx.Err()
				return
			}
			dirty, err := workspaceDirty(scanCtx, workspaces[i].Path)
			checks[i].statusErr = err
			workspaces[i].Dirty = dirty
			if err != nil {
				return
			}
			identity, identityErr := RepositoryIdentity(scanCtx, workspaces[i].Path)
			checks[i].identityErr = identityErr
			checks[i].commonDir = identity.CommonDir
		}(i)
	}
	wg.Wait()

	for i := range workspaces {
		workspace := &workspaces[i]
		check := checks[i]
		switch {
		case workspace.Bare:
			workspace.DisabledReason = "bare repository"
		case workspace.Prunable:
			workspace.DisabledReason = "prunable worktree"
		case check.canonicalErr != nil:
			workspace.DisabledReason = "worktree path is missing or inaccessible"
		case workspace.Locked:
			workspace.DisabledReason = "locked worktree"
		case workspace.Detached || workspace.Ref == "":
			workspace.DisabledReason = "detached HEAD"
		case !strings.HasPrefix(workspace.Ref, "refs/heads/"):
			workspace.DisabledReason = "worktree is not on a local branch"
		case check.statusErr != nil:
			workspace.DisabledReason = "worktree status is unavailable"
		default:
			if check.identityErr != nil || check.commonDir != repository.CommonDir {
				workspace.DisabledReason = "worktree belongs to a different repository"
			}
		}
		workspace.Healthy = workspace.DisabledReason == ""
	}
	return workspaces, nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (output *boundedOutput) Write(value []byte) (int, error) {
	original := len(value)
	remaining := output.limit - output.buffer.Len()
	if remaining > 0 {
		if len(value) > remaining {
			_, _ = output.buffer.Write(value[:remaining])
		} else {
			_, _ = output.buffer.Write(value)
		}
	}
	if original > remaining {
		output.overflow = true
	}
	return original, nil
}

func (c *Client) runWorkspaceList(ctx context.Context) ([]byte, error) {
	cmd := workspaceReadOnlyGitCommand(ctx, c.dir, "worktree", "list", "--porcelain", "-z")
	stdout := &boundedOutput{limit: workspaceListOutputLimit}
	stderr := &boundedOutput{limit: 8 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.buffer.String())
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("git worktree list: %s", message)
	}
	if stdout.overflow {
		return nil, fmt.Errorf("git output exceeds the %d-byte safety limit", workspaceListOutputLimit)
	}
	return stdout.buffer.Bytes(), nil
}

// workspaceDirty stops after Git emits the first porcelain byte. "normal"
// untracked reporting represents an untracked directory once instead of
// enumerating every descendant, which keeps /git and /resume responsive on
// generated dependency trees.
func workspaceDirty(ctx context.Context, dir string) (bool, error) {
	statusCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := workspaceReadOnlyGitCommand(statusCtx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=normal", "--no-renames")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	stderr := &boundedOutput{limit: 8 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("git status: %w", err)
	}
	var first [1]byte
	n, readErr := stdout.Read(first[:])
	if n > 0 {
		cancel()
		_ = cmd.Wait()
		return true, nil
	}
	waitErr := cmd.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) && ctx.Err() == nil {
		return false, fmt.Errorf("git status: %w", readErr)
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if waitErr != nil {
		message := strings.TrimSpace(stderr.buffer.String())
		if message == "" {
			message = waitErr.Error()
		}
		return false, fmt.Errorf("git status: %s", message)
	}
	return false, nil
}

// workspaceReadOnlyGitCommand is the trust boundary for metadata refreshes.
// `git status` consults core.fsmonitor and can otherwise execute an arbitrary
// repository-configured program. These commands must stay observational: they
// neither run hooks nor take optional repository locks, and they can never
// prompt on a hidden terminal.
func workspaceReadOnlyGitCommand(ctx context.Context, dir string, args ...string) *exec.Cmd {
	// Keep the security-critical prefix local and immutable. An empty hooks path
	// is portable across Unix and Windows.
	safeArgs := make([]string, 0, 4+len(args))
	safeArgs = append(safeArgs,
		"-c", "core.fsmonitor=false",
		"-c", "core.hooksPath=",
	)
	safeArgs = append(safeArgs, args...)
	cmd := exec.CommandContext(ctx, "git", safeArgs...)
	cmd.Dir = dir
	cmd.Env = mergeEnvironment(map[string]string{
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_TERMINAL_PROMPT": "0",
	})
	return cmd
}

func parseWorktreePorcelainZ(out []byte) ([]Workspace, error) {
	fields, err := nulFields(out)
	if err != nil {
		return nil, err
	}
	var result []Workspace
	var current *Workspace
	flush := func() error {
		if current == nil {
			return nil
		}
		if current.Path == "" {
			return errors.New("worktree record has no path")
		}
		if current.Head == "" && !current.Bare {
			return fmt.Errorf("worktree %q has no HEAD", current.Path)
		}
		result = append(result, *current)
		current = nil
		return nil
	}
	for _, field := range fields {
		if field == "" {
			if err := flush(); err != nil {
				return nil, err
			}
			continue
		}
		key, value, hasValue := strings.Cut(field, " ")
		if key == "worktree" {
			if current != nil {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			if !hasValue || value == "" {
				return nil, errors.New("worktree record has an empty path")
			}
			current = &Workspace{Path: value}
			continue
		}
		if current == nil {
			return nil, fmt.Errorf("worktree attribute %q precedes a path", key)
		}
		switch key {
		case "HEAD":
			if !hasValue || !validObjectID(value) {
				return nil, fmt.Errorf("worktree %q has invalid HEAD %q", current.Path, value)
			}
			current.Head = value
		case "branch":
			if !hasValue || value == "" {
				return nil, fmt.Errorf("worktree %q has an empty branch", current.Path)
			}
			current.Ref = value
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "detached":
			current.Detached = true
		case "bare":
			current.Bare = true
		case "locked":
			current.Locked = true
			if hasValue {
				current.LockReason = value
			}
		case "prunable":
			current.Prunable = true
			if hasValue {
				current.PrunableReason = value
			}
		default:
			// Porcelain format is stable, but Git may add attributes. Unknown
			// fields cannot make a recognized unsafe state healthy, so retain
			// forward compatibility by ignoring them.
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
}

// ValidateBranchName delegates the complete ref grammar to Git.
func (c *Client) ValidateBranchName(ctx context.Context, name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.IndexByte(name, 0) >= 0 {
		return fmt.Errorf("branch name %q is not a valid Git branch", name)
	}
	if _, err := c.run(ctx, "check-ref-format", "refs/heads/"+name); err != nil {
		return fmt.Errorf("branch name %q is not a valid Git branch: %w", name, err)
	}
	return nil
}

// HeadOID resolves the exact commit used as the start point for a new linked
// worktree. Uncommitted source changes are intentionally excluded.
func (c *Client) HeadOID(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	oid := strings.TrimSuffix(string(out), "\n")
	if !validObjectID(oid) {
		return "", fmt.Errorf("resolve HEAD: Git returned invalid object ID %q", oid)
	}
	return oid, nil
}

// CreateLinkedWorktree creates a new local branch at the explicit start OID.
// Hooks are disabled, the checkout is locked while it is verified, and no
// force or remote-branch guessing is permitted.
func (c *Client) CreateLinkedWorktree(ctx context.Context, path, branch, startOID string) (Workspace, error) {
	if err := c.ValidateBranchName(ctx, branch); err != nil {
		return Workspace{}, err
	}
	if !validObjectID(startOID) {
		return Workspace{}, fmt.Errorf("create worktree: invalid start OID %q", startOID)
	}
	if !filepath.IsAbs(path) {
		return Workspace{}, errors.New("create worktree: path must be absolute")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Workspace{}, fmt.Errorf("create worktree: target %q already exists", path)
		}
		return Workspace{}, fmt.Errorf("create worktree: inspect target %q: %w", path, err)
	}
	if superOut, err := c.run(ctx, "rev-parse", "--show-superproject-working-tree"); err != nil {
		return Workspace{}, fmt.Errorf("create worktree: inspect superproject: %w", err)
	} else if strings.TrimSuffix(string(superOut), "\n") != "" {
		return Workspace{}, errors.New("create worktree: submodule repositories are not supported")
	}
	ref := "refs/heads/" + branch
	if _, err := c.run(ctx, "rev-parse", "--verify", ref); err == nil {
		return Workspace{}, fmt.Errorf("create worktree: branch %q already exists", branch)
	}
	zeroOID := strings.Repeat("0", len(startOID))
	if _, err := c.run(ctx, "update-ref", ref, startOID, zeroOID); err != nil {
		return Workspace{}, fmt.Errorf("create worktree: create branch %q at %s: %w", branch, startOID, err)
	}
	created := Workspace{Path: path, Ref: ref, Branch: branch, Head: startOID}
	hooksDir, err := os.MkdirTemp(filepath.Dir(path), ".maestro-empty-hooks-")
	if err != nil {
		cleanupErr := c.DeleteBranchIfOID(context.Background(), branch, startOID)
		return Workspace{}, errors.Join(fmt.Errorf("create worktree: create isolated hooks directory: %w", err), cleanupErr)
	}
	if err := os.Chmod(hooksDir, 0o700); err != nil {
		_ = os.RemoveAll(hooksDir)
		cleanupErr := c.DeleteBranchIfOID(context.Background(), branch, startOID)
		return Workspace{}, errors.Join(fmt.Errorf("create worktree: protect isolated hooks directory: %w", err), cleanupErr)
	}
	defer os.RemoveAll(hooksDir)
	if _, err := c.runWithEnv(ctx, map[string]string{
		"GIT_TERMINAL_PROMPT": "0",
	}, "-c", "core.fsmonitor=false", "-c", "core.hooksPath="+hooksDir, "worktree", "add", "--lock", path, branch); err != nil {
		cleanupErr := c.cleanupFailedWorktree(context.Background(), created)
		return Workspace{}, errors.Join(fmt.Errorf("create worktree: %w", err), cleanupErr)
	}
	cleanup := func(cause error) (Workspace, error) {
		_, _ = c.run(context.Background(), "worktree", "unlock", path)
		cleanupErr := c.CleanupCreatedWorktree(context.Background(), created)
		return Workspace{}, errors.Join(cause, cleanupErr)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return cleanup(fmt.Errorf("create worktree: protect managed checkout: %w", err))
	}
	workspaces, err := c.ListWorkspaces(ctx)
	if err != nil {
		return cleanup(fmt.Errorf("create worktree: verify registration: %w", err))
	}
	verified, ok := findWorkspace(workspaces, path)
	if !ok || verified.Ref != created.Ref || verified.Head != startOID || verified.Detached || verified.Prunable {
		return cleanup(errors.New("create worktree: Git registration did not match the requested branch and OID"))
	}
	if _, err := c.run(ctx, "worktree", "unlock", path); err != nil {
		return cleanup(fmt.Errorf("create worktree: unlock verified checkout: %w", err))
	}
	workspaces, err = c.ListWorkspaces(ctx)
	if err != nil {
		return cleanup(fmt.Errorf("create worktree: verify unlocked checkout: %w", err))
	}
	verified, ok = findWorkspace(workspaces, path)
	if !ok || !verified.Healthy || verified.Dirty {
		return cleanup(errors.New("create worktree: new checkout is not a clean healthy workspace"))
	}
	return verified, nil
}

func (c *Client) cleanupFailedWorktree(ctx context.Context, expected Workspace) error {
	workspaces, err := c.ListWorkspaces(ctx)
	if err == nil {
		if registered, ok := findWorkspace(workspaces, expected.Path); ok {
			return c.CleanupCreatedWorktree(ctx, registered)
		}
	}
	oid, oidErr := c.BranchOID(ctx, expected.Branch)
	if oidErr != nil {
		return nil
	}
	if oid != expected.Head {
		return fmt.Errorf("cleanup failed worktree: preserve concurrently changed branch %q", expected.Branch)
	}
	return c.DeleteBranchIfOID(ctx, expected.Branch, expected.Head)
}

// CleanupCreatedWorktree removes only an unchanged, clean worktree and then
// CAS-deletes its branch at the expected OID.
func (c *Client) CleanupCreatedWorktree(ctx context.Context, expected Workspace) error {
	workspaces, err := c.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("cleanup worktree: %w", err)
	}
	current, ok := findWorkspace(workspaces, expected.Path)
	if !ok {
		return fmt.Errorf("cleanup worktree: preserve %q because it is no longer registered", expected.Path)
	}
	if current.Ref != expected.Ref || current.Head != expected.Head || current.Dirty || current.Detached || current.Prunable {
		return fmt.Errorf("cleanup worktree: preserve changed workspace %q", expected.Path)
	}
	if current.Locked {
		if _, err := c.run(ctx, "worktree", "unlock", current.Path); err != nil {
			return fmt.Errorf("cleanup worktree: unlock %q: %w", current.Path, err)
		}
	}
	if err := c.WorktreeRemove(ctx, current.Path); err != nil {
		return fmt.Errorf("cleanup worktree: %w", err)
	}
	if err := c.DeleteBranchIfOID(ctx, expected.Branch, expected.Head); err != nil {
		return fmt.Errorf("cleanup worktree: %w", err)
	}
	return nil
}

func findWorkspace(workspaces []Workspace, path string) (Workspace, bool) {
	want := comparablePath(path)
	for _, workspace := range workspaces {
		candidate := comparablePath(workspace.Path)
		if candidate == want {
			return workspace, true
		}
	}
	return Workspace{}, false
}

func comparablePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	abs = filepath.Clean(abs)
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	var suffix []string
	probe := abs
	for {
		parent := filepath.Dir(probe)
		if parent == probe {
			return abs
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = parent
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr != nil {
			continue
		}
		for i := len(suffix) - 1; i >= 0; i-- {
			resolved = filepath.Join(resolved, suffix[i])
		}
		return filepath.Clean(resolved)
	}
}
