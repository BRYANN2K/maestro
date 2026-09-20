// Package git wraps the git CLI for the orchestration flows: branches,
// worktrees, diffs, status, and validated commits. The git binary must be on
// PATH; everything is exercised through exec.CommandContext so cancellation
// propagates to the child process.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// WorktreeDiff is review evidence, not a generic artifact transport. A patch
	// beyond this size would both exceed useful model context and make a single
	// Git subprocess grow Maestro's heap without bound. Refuse it explicitly;
	// callers must split or otherwise reduce the change instead of reviewing a
	// silently truncated semantic diff.
	maxWorktreeDiffBytes          = 2 << 20
	maxWorktreeDiffFileBytes      = int64(16 << 20)
	maxWorktreeDiffAggregateBytes = int64(32 << 20)
	maxWorktreeDiffDuration       = 30 * time.Second
	maxGitListOutputBytes         = 16 << 20
	maxGitIndexOutputBytes        = 32 << 20
	maxGitDiagnosticBytes         = 64 << 10
	maxGitInventoryFields         = 1 << 17
	maxGitIndexRecords            = 1 << 19
)

var errGitCommandOutputLimit = errors.New("git command output limit exceeded")

// worktreeEvidenceArgs disables repository-configured filesystem monitors for
// every Git command in the review snapshot path. A filesystem monitor is an
// executable hook; review evidence and submodule safety checks must never run
// repository-controlled programs merely to inspect the checkout.
func worktreeEvidenceArgs(args ...string) []string {
	out := make([]string, 0, len(args)+2)
	out = append(out, "-c", "core.fsmonitor=false")
	return append(out, args...)
}

// ErrDetachedHEAD is returned when an operation requires a named current
// branch but the repository is checked out at a commit directly.
var ErrDetachedHEAD = errors.New("detached HEAD has no current branch")

// Client runs git commands inside a single repository directory.
type Client struct {
	dir     string
	ceiling string
}

// New returns a Client rooted at dir.
func New(dir string) *Client { return &Client{dir: dir} }

// NewProject returns a client confined to the selected project directory.
// Maestro uses it for the initial workspace so an unrelated repository in an
// ancestor (most notably ~/.git) cannot silently capture an empty child
// project. A repository rooted at dir remains discoverable.
func NewProject(dir string) *Client {
	canonical, err := canonicalPath(dir)
	if err != nil {
		canonical = filepath.Clean(dir)
	}
	return &Client{dir: canonical, ceiling: filepath.Dir(canonical)}
}

// Dir returns the repository directory.
func (c *Client) Dir() string { return c.dir }

// RepositoryRoot resolves dir to the canonical top-level checkout. Keeping
// this resolution in the Git layer prevents pathspecs such as "." from
// silently limiting a review to the caller's current subdirectory.
func RepositoryRoot(ctx context.Context, dir string) (string, error) {
	out, err := New(dir).run(ctx, "rev-parse", "--show-toplevel")
	return repositoryRootFromOutput(dir, out, err)
}

func worktreeEvidenceRoot(ctx context.Context, dir string) (string, error) {
	out, err := New(dir).run(ctx, worktreeEvidenceArgs("rev-parse", "--show-toplevel")...)
	return repositoryRootFromOutput(dir, out, err)
}

func repositoryRootFromOutput(dir string, out []byte, runErr error) (string, error) {
	if runErr != nil {
		return "", fmt.Errorf("resolve repository root from %q: %w", dir, runErr)
	}
	// rev-parse terminates the path with one LF. Remove only that delimiter:
	// TrimSpace would corrupt legal repository names ending in spaces or
	// containing newlines.
	root := strings.TrimSuffix(string(out), "\n")
	if root == "" {
		return "", fmt.Errorf("resolve repository root from %q: git returned an empty path", dir)
	}
	canonical, err := canonicalPath(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root from %q: %w", dir, err)
	}
	return canonical, nil
}

// ProjectRoot resolves a normal Git checkout to its top level while treating
// a descendant of a Git-backed home directory as its own project. A ~/.git is
// commonly used for dotfiles and must not make every new folder share the
// home repository's sessions, files, or Git operations.
func ProjectRoot(ctx context.Context, dir string) (string, error) {
	requested, err := canonicalPath(dir)
	if err != nil {
		return "", err
	}
	root, err := RepositoryRoot(ctx, requested)
	if err != nil {
		return requested, nil
	}
	if root != requested {
		home, homeErr := os.UserHomeDir()
		if homeErr == nil {
			canonicalHome, canonicalErr := canonicalPath(home)
			if canonicalErr == nil && root == canonicalHome {
				return requested, nil
			}
		}
	}
	return root, nil
}

// RepositoryRoot returns the canonical top-level checkout for this client.
func (c *Client) RepositoryRoot(ctx context.Context) (string, error) {
	return RepositoryRoot(ctx, c.dir)
}

// StatusEntry is one entry from git status --porcelain.
type StatusEntry struct {
	Path       string
	OldPath    string // set when the entry is renamed or copied
	IndexState byte   // staged state: ' ' if unmodified
	Worktree   byte   // unstaged state: ' ' if unmodified
}

// Status summarizes the working tree of the repository.
type Status struct {
	Branch string
	Dirty  bool
	Files  []StatusEntry
}

// FileChange is one file touched between two revisions, as reported by
// git diff --name-status.
type FileChange struct {
	Path    string
	Type    string // M added deleted renamed copied modified-untracked
	OldPath string // set when Type is renamed or copied
}

// CurrentBranch returns the checked-out branch name.
func (c *Client) CurrentBranch(ctx context.Context) (string, error) {
	// symbolic-ref resolves the branch even before the repository has its first
	// commit. rev-parse HEAD does not, which made Maestro unable to start in a
	// freshly initialized repository.
	out, err := c.run(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		// A detached HEAD is still a valid commit, but it has no symbolic branch.
		// Preserve the typed error used by branch-sensitive workflows without
		// misclassifying a non-repository or an unavailable Git directory.
		if _, verifyErr := c.run(ctx, "rev-parse", "--verify", "HEAD"); verifyErr == nil {
			return "", ErrDetachedHEAD
		}
		return "", fmt.Errorf("current branch: %w", err)
	}
	branch := strings.TrimSuffix(string(out), "\n")
	if branch == "" || branch == "HEAD" {
		return "", ErrDetachedHEAD
	}
	return branch, nil
}

// IsRepo reports whether dir is inside a git repository.
func (c *Client) IsRepo(ctx context.Context) bool {
	_, err := c.run(ctx, "rev-parse", "--git-dir")
	return err == nil
}

// Branch creates and switches to a new branch named name.
func (c *Client) Branch(ctx context.Context, name string) error {
	if err := validBranchName(name); err != nil {
		return err
	}
	if _, err := c.run(ctx, "switch", "-c", name); err != nil {
		return fmt.Errorf("create branch %s: %w", name, err)
	}
	return nil
}

// DeleteBranch deletes a local branch that is not currently checked out.
func (c *Client) DeleteBranch(ctx context.Context, name string) error {
	if err := validBranchName(name); err != nil {
		return err
	}
	if _, err := c.run(ctx, "branch", "-D", name); err != nil {
		return fmt.Errorf("delete branch %s: %w", name, err)
	}
	return nil
}

// BranchOID resolves the full commit object ID currently stored in a local
// branch ref. The fully qualified ref prevents a tag/path ambiguity.
func (c *Client) BranchOID(ctx context.Context, name string) (string, error) {
	if err := validBranchName(name); err != nil {
		return "", err
	}
	ref := "refs/heads/" + name
	out, err := c.run(ctx, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve branch %s: %w", name, err)
	}
	oid := strings.TrimSuffix(string(out), "\n")
	if !validObjectID(oid) {
		return "", fmt.Errorf("resolve branch %s: Git returned invalid object ID %q", name, oid)
	}
	return oid, nil
}

// DeleteBranchIfOID atomically deletes a local branch only while its ref
// still contains expectedOID. git update-ref supplies the compare-and-swap;
// a concurrent commit therefore survives and is reported to the caller.
func (c *Client) DeleteBranchIfOID(ctx context.Context, name, expectedOID string) error {
	if err := validBranchName(name); err != nil {
		return err
	}
	if !validObjectID(expectedOID) {
		return fmt.Errorf("delete branch %s: invalid Git object ID %q", name, expectedOID)
	}
	ref := "refs/heads/" + name
	if _, err := c.run(ctx, "update-ref", "-d", ref, expectedOID); err != nil {
		return fmt.Errorf("delete branch %s only at %s: %w", name, expectedOID, err)
	}
	return nil
}

// Switch checks out an existing branch.
func (c *Client) Switch(ctx context.Context, name string) error {
	if err := validBranchName(name); err != nil {
		return err
	}
	if _, err := c.run(ctx, "switch", name); err != nil {
		return fmt.Errorf("switch to %s: %w", name, err)
	}
	return nil
}

// Merge merges branch into the current checkout.
func (c *Client) Merge(ctx context.Context, branch string) error {
	if err := validBranchName(branch); err != nil {
		return err
	}
	if _, err := c.run(ctx, "merge", branch); err != nil {
		return fmt.Errorf("merge %s: %w", branch, err)
	}
	return nil
}

// AbortMerge restores a checkout after a conflicted merge attempt. Callers
// use it only as best-effort recovery when Merge returned an error.
func (c *Client) AbortMerge(ctx context.Context) error {
	if _, err := c.run(ctx, "merge", "--abort"); err != nil {
		return fmt.Errorf("abort merge: %w", err)
	}
	return nil
}

// WorktreeAdd creates a new worktree at path on a new branch named branch.
func (c *Client) WorktreeAdd(ctx context.Context, path, branch string) error {
	if err := validBranchName(branch); err != nil {
		return err
	}
	if _, err := c.run(ctx, "worktree", "add", "-b", branch, path); err != nil {
		return fmt.Errorf("add worktree %s on %s: %w", path, branch, err)
	}
	return nil
}

// WorktreeRemove removes the worktree at path.
func (c *Client) WorktreeRemove(ctx context.Context, path string) error {
	if _, err := c.run(ctx, "worktree", "remove", path); err != nil {
		return fmt.Errorf("remove worktree %s: %w", path, err)
	}
	return nil
}

// HasWorktree reports whether path is registered as a worktree of this
// repository. It is used before trusting a persisted session path.
func (c *Client) HasWorktree(ctx context.Context, path string) (bool, error) {
	want, err := canonicalPath(path)
	if err != nil {
		return false, nil
	}
	out, err := c.run(ctx, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return false, fmt.Errorf("list worktrees: %w", err)
	}
	fields, err := nulFields(out)
	if err != nil {
		return false, fmt.Errorf("list worktrees: %w", err)
	}
	for _, field := range fields {
		if !strings.HasPrefix(field, "worktree ") {
			continue
		}
		candidate, err := canonicalPath(strings.TrimPrefix(field, "worktree "))
		if err == nil && candidate == want {
			return true, nil
		}
	}
	return false, nil
}

func canonicalPath(path string) (string, error) {
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

// AllChanges returns the tracked changes vs HEAD plus untracked files —
// the full set of paths a sub-agent touched (diff alone misses untracked).
// Status is requested with --untracked-files=all, so untracked directories are
// represented by their individual files without parsing or walking quoted paths.
func (c *Client) AllChanges(ctx context.Context) ([]FileChange, error) {
	changes, err := c.DiffNameStatus(ctx, "HEAD")
	if err != nil {
		return nil, err
	}
	untracked, err := c.UntrackedFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("all changes untracked files: %w", err)
	}
	for _, path := range untracked {
		changes = append(changes, FileChange{Path: path, Type: "A"})
	}
	return changes, nil
}

// DiffNameStatus returns the files changed between base and the working
// tree. An empty base means the working tree vs the index.
func (c *Client) DiffNameStatus(ctx context.Context, base string) ([]FileChange, error) {
	args := []string{"diff", "--name-status", "-z"}
	if base != "" {
		args = append(args, base)
	}
	args = append(args, "--")
	out, exceeded, err := c.runWithEnvOutputLimit(ctx, nil, maxGitListOutputBytes, args...)
	if err != nil {
		return nil, fmt.Errorf("diff name-status: %w", err)
	}
	if exceeded {
		return nil, fmt.Errorf("diff name-status: output exceeds %d-byte change inventory limit", maxGitListOutputBytes)
	}
	if fields := bytes.Count(out, []byte{0}); fields > maxGitInventoryFields {
		return nil, fmt.Errorf("diff name-status: output contains %d fields; change inventory limit is %d", fields, maxGitInventoryFields)
	}
	changes, err := parseNameStatusZ(out)
	if err != nil {
		return nil, fmt.Errorf("diff name-status: %w", err)
	}
	return changes, nil
}

// UntrackedFiles lists untracked, non-ignored files in the working tree.
func (c *Client) UntrackedFiles(ctx context.Context) ([]string, error) {
	out, exceeded, err := c.runWithEnvOutputLimit(ctx, nil, maxGitListOutputBytes, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("ls-files others: %w", err)
	}
	if exceeded {
		return nil, fmt.Errorf("ls-files others: output exceeds %d-byte change inventory limit", maxGitListOutputBytes)
	}
	if fields := bytes.Count(out, []byte{0}); fields > maxGitInventoryFields {
		return nil, fmt.Errorf("ls-files others: output contains %d paths; change inventory limit is %d", fields, maxGitInventoryFields)
	}
	paths, err := nulFields(out)
	if err != nil {
		return nil, fmt.Errorf("ls-files others: %w", err)
	}
	return paths, nil
}

// TrackedFiles lists the paths currently represented by the index. In an
// unborn repository this is the exact candidate set for the first commit.
func (c *Client) TrackedFiles(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "ls-files", "-z")
	if err != nil {
		return nil, fmt.Errorf("ls-files: %w", err)
	}
	paths, err := nulFields(out)
	if err != nil {
		return nil, fmt.Errorf("ls-files: %w", err)
	}
	return paths, nil
}

// DiffUnified returns the unified diff between base and the working tree.
func (c *Client) DiffUnified(ctx context.Context, base string) (string, error) {
	args := []string{"diff"}
	if base != "" {
		args = append(args, base)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("diff unified: %w", err)
	}
	return string(out), nil
}

// WorktreeDiff returns a binary-safe patch for the complete Git-visible
// filesystem versus base, including non-ignored untracked files. A private
// index and object store overlay the worktree on base, so the user's real Git
// state and working files remain untouched and partially staged state cannot
// hide evidence.
func (c *Client) WorktreeDiff(ctx context.Context, base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", errors.New("worktree diff: base revision is required")
	}
	workCtx, cancel := context.WithTimeout(ctx, maxWorktreeDiffDuration)
	defer cancel()

	root, err := worktreeEvidenceRoot(workCtx, c.dir)
	if err != nil {
		return "", fmt.Errorf("worktree diff: %w", err)
	}
	rooted := New(root)
	if err := checkSubmodulesCleanAtRoot(workCtx, root); err != nil {
		return "", fmt.Errorf("worktree diff: %w", err)
	}
	realObjectDir, err := rooted.objectDirectory(workCtx)
	if err != nil {
		return "", fmt.Errorf("worktree diff: resolve object store: %w", err)
	}
	tempRoot, err := os.MkdirTemp("", "maestro-worktree-diff-")
	if err != nil {
		return "", fmt.Errorf("worktree diff: create private index: %w", err)
	}
	defer os.RemoveAll(tempRoot)

	privateObjectDir := filepath.Join(tempRoot, "objects")
	if err := os.Mkdir(privateObjectDir, 0o700); err != nil {
		return "", fmt.Errorf("worktree diff: create private object store: %w", err)
	}
	alternates, err := isolatedObjectAlternates(realObjectDir)
	if err != nil {
		return "", fmt.Errorf("worktree diff: configure private object store: %w", err)
	}
	env := map[string]string{
		"GIT_INDEX_FILE":                   filepath.Join(tempRoot, "index"),
		"GIT_OBJECT_DIRECTORY":             privateObjectDir,
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": alternates,
	}
	if err := rooted.runWithEnvNoOutput(workCtx, env, worktreeEvidenceArgs("read-tree", base)...); err != nil {
		return "", fmt.Errorf("worktree diff: seed private index: %w", err)
	}

	paths, err := rooted.worktreeDiffPathInventory(workCtx, env)
	if err != nil {
		return "", fmt.Errorf("worktree diff: inventory filesystem: %w", err)
	}
	worktreeRoot, err := os.OpenRoot(root)
	if err != nil {
		return "", fmt.Errorf("worktree diff: open repository root: %w", err)
	}
	defer worktreeRoot.Close()
	if err := preflightWorktreeDiffPaths(workCtx, worktreeRoot, paths, false); err != nil {
		return "", fmt.Errorf("worktree diff: preflight filesystem: %w", err)
	}
	// Git status and add both apply clean/process attributes while comparing
	// worktree bytes with the index. Check every path before either command so
	// review evidence never executes a repository-configured content filter.
	if err := rooted.rejectWorktreeDiffFilters(workCtx, env, paths); err != nil {
		return "", fmt.Errorf("worktree diff: %w", err)
	}
	changedPaths, err := rooted.worktreeDiffChangedPaths(workCtx, env, paths)
	if err != nil {
		return "", fmt.Errorf("worktree diff: inventory changes: %w", err)
	}
	if err := preflightWorktreeDiffPaths(workCtx, worktreeRoot, changedPaths, true); err != nil {
		return "", fmt.Errorf("worktree diff: preflight changed files: %w", err)
	}
	// Re-evaluate immediately before the mutating Git command. The first pass
	// protects status; this pass also detects attribute edits made while status
	// was running.
	if err := rooted.rejectWorktreeDiffFilters(workCtx, env, changedPaths); err != nil {
		return "", fmt.Errorf("worktree diff: %w", err)
	}
	if len(changedPaths) > 0 {
		if err := rooted.runWithEnvInputNoOutput(workCtx, env, nulPathList(changedPaths),
			worktreeEvidenceArgs("add", "-A", "--pathspec-from-file=-", "--pathspec-file-nul")...); err != nil {
			return "", fmt.Errorf("worktree diff: snapshot filesystem: %w", err)
		}
	}
	out, exceeded, err := rooted.runWithEnvOutputLimit(workCtx, env, maxWorktreeDiffBytes,
		worktreeEvidenceArgs("diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", base, "--")...)
	if err != nil {
		return "", fmt.Errorf("worktree diff: render patch: %w", err)
	}
	if exceeded {
		return "", fmt.Errorf("worktree diff: patch exceeds review evidence limit of %d bytes; split or reduce the change before review", maxWorktreeDiffBytes)
	}
	return string(out), nil
}

func (c *Client) objectDirectory(ctx context.Context) (string, error) {
	out, exceeded, err := c.runWithEnvOutputLimit(ctx, nil, maxGitDiagnosticBytes,
		worktreeEvidenceArgs("rev-parse", "--path-format=absolute", "--git-path", "objects")...)
	if err != nil {
		return "", err
	}
	if exceeded {
		return "", fmt.Errorf("git object directory exceeds %d-byte path limit", maxGitDiagnosticBytes)
	}
	raw, err := gitOutputPath(out)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("git returned non-absolute object path %q", boundedGitDiagnostic(raw))
	}
	resolved := filepath.Clean(raw)
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("git object path %q is not a directory", boundedGitDiagnostic(resolved))
	}
	return resolved, nil
}

func isolatedObjectAlternates(realObjectDir string) (string, error) {
	quoted, err := quoteGitPathListEntry(realObjectDir)
	if err != nil {
		return "", err
	}
	if inherited, ok := os.LookupEnv("GIT_ALTERNATE_OBJECT_DIRECTORIES"); ok && inherited != "" {
		if len(inherited) > maxGitListOutputBytes {
			return "", fmt.Errorf("inherited alternate object path list exceeds %d bytes", maxGitListOutputBytes)
		}
		quoted += string(os.PathListSeparator) + inherited
	}
	return quoted, nil
}

// quoteGitPathListEntry uses Git's documented C-style quoting so repositories
// whose absolute object paths contain ':' on Unix, ';' on Windows, quotes, or
// control bytes remain one alternate entry.
func quoteGitPathListEntry(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("invalid empty or NUL-containing Git object path")
	}
	var quoted strings.Builder
	quoted.Grow(len(path) + 2)
	quoted.WriteByte('"')
	for i := 0; i < len(path); i++ {
		value := path[i]
		switch value {
		case '\\', '"':
			quoted.WriteByte('\\')
			quoted.WriteByte(value)
		case '\a':
			quoted.WriteString(`\a`)
		case '\b':
			quoted.WriteString(`\b`)
		case '\t':
			quoted.WriteString(`\t`)
		case '\n':
			quoted.WriteString(`\n`)
		case '\v':
			quoted.WriteString(`\v`)
		case '\f':
			quoted.WriteString(`\f`)
		case '\r':
			quoted.WriteString(`\r`)
		default:
			if value < 0x20 || value == 0x7f {
				quoted.WriteByte('\\')
				quoted.WriteByte('0' + (value >> 6))
				quoted.WriteByte('0' + ((value >> 3) & 7))
				quoted.WriteByte('0' + (value & 7))
			} else {
				quoted.WriteByte(value)
			}
		}
	}
	quoted.WriteByte('"')
	return quoted.String(), nil
}

func (c *Client) worktreeDiffPathInventory(ctx context.Context, env map[string]string) ([]string, error) {
	out, exceeded, err := c.runWithEnvOutputLimit(ctx, env, maxGitListOutputBytes,
		worktreeEvidenceArgs("ls-files", "--cached", "--others", "--exclude-standard", "-z", "--")...)
	if err != nil {
		return nil, err
	}
	if exceeded {
		return nil, fmt.Errorf("path inventory exceeds %d bytes", maxGitListOutputBytes)
	}
	if fields := bytes.Count(out, []byte{0}); fields > maxGitInventoryFields {
		return nil, fmt.Errorf("path inventory contains %d paths; limit is %d", fields, maxGitInventoryFields)
	}
	paths, err := nulFields(out)
	if err != nil {
		return nil, err
	}
	return deduplicateGitPaths(paths), nil
}

func preflightWorktreeDiffPaths(ctx context.Context, root *os.Root, paths []string, enforceSizeLimits bool) error {
	var aggregate int64
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := root.Lstat(filepath.FromSlash(path))
		if errors.Is(err, os.ErrNotExist) {
			continue // a tracked deletion is a legitimate changed path
		}
		if err != nil {
			return fmt.Errorf("inspect %q: %w", boundedGitDiagnostic(path), err)
		}
		mode := info.Mode()
		if mode.IsDir() {
			// Checked-out submodules and embedded repositories are represented by
			// a directory path but add only their Git commit ID to the index.
			continue
		}
		if !mode.IsRegular() && mode&os.ModeSymlink == 0 {
			return fmt.Errorf("path %q has unsupported special file mode %s", boundedGitDiagnostic(path), mode)
		}
		if !enforceSizeLimits {
			continue
		}
		size := info.Size()
		if size < 0 {
			return fmt.Errorf("path %q has an invalid negative size", boundedGitDiagnostic(path))
		}
		if size > maxWorktreeDiffFileBytes {
			return fmt.Errorf("path %q is %d bytes; per-file review snapshot limit is %d bytes", boundedGitDiagnostic(path), size, maxWorktreeDiffFileBytes)
		}
		if size > maxWorktreeDiffAggregateBytes-aggregate {
			return fmt.Errorf("changed file set exceeds aggregate review snapshot limit of %d bytes", maxWorktreeDiffAggregateBytes)
		}
		aggregate += size
	}
	return nil
}

func (c *Client) rejectWorktreeDiffFilters(ctx context.Context, env map[string]string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	out, exceeded, err := c.runWithEnvInputOutputLimit(ctx, env, nulPathList(paths), maxGitListOutputBytes,
		worktreeEvidenceArgs("check-attr", "-z", "--stdin", "filter")...)
	if err != nil {
		return fmt.Errorf("inspect content filters: %w", err)
	}
	if exceeded {
		return fmt.Errorf("content-filter inventory exceeds %d bytes", maxGitListOutputBytes)
	}
	fields, err := nulFields(out)
	if err != nil {
		return fmt.Errorf("inspect content filters: %w", err)
	}
	if len(fields) != len(paths)*3 {
		return fmt.Errorf("inspect content filters: Git returned %d fields for %d paths", len(fields), len(paths))
	}
	for i, path := range paths {
		gotPath, attribute, value := fields[i*3], fields[i*3+1], fields[i*3+2]
		if gotPath != path || attribute != "filter" {
			return fmt.Errorf("inspect content filters: malformed result for path %q", boundedGitDiagnostic(path))
		}
		if value != "unspecified" && value != "unset" {
			return fmt.Errorf("content filter %q applies to path %q; review snapshots refuse clean/process filters", boundedGitDiagnostic(value), boundedGitDiagnostic(path))
		}
	}
	return nil
}

func (c *Client) worktreeDiffChangedPaths(ctx context.Context, env map[string]string, allowedPaths []string) ([]string, error) {
	out, exceeded, err := c.runWithEnvOutputLimit(ctx, env, maxGitListOutputBytes,
		worktreeEvidenceArgs("status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none", "--no-renames")...)
	if err != nil {
		return nil, err
	}
	if exceeded {
		return nil, fmt.Errorf("changed-path inventory exceeds %d bytes", maxGitListOutputBytes)
	}
	if fields := bytes.Count(out, []byte{0}); fields > maxGitInventoryFields {
		return nil, fmt.Errorf("changed-path inventory contains %d fields; limit is %d", fields, maxGitInventoryFields)
	}
	entries, err := parseStatusZ(out)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]struct{}, len(allowedPaths))
	for _, path := range allowedPaths {
		allowed[path] = struct{}{}
	}
	paths := make([]string, 0, len(entries)*2)
	for _, entry := range entries {
		for _, path := range []string{entry.Path, entry.OldPath} {
			if path == "" {
				continue
			}
			if _, ok := allowed[path]; !ok {
				return nil, fmt.Errorf("path %q appeared after the filter-safe inventory; retry the snapshot", boundedGitDiagnostic(path))
			}
			paths = append(paths, path)
		}
	}
	return deduplicateGitPaths(paths), nil
}

func deduplicateGitPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		unique = append(unique, path)
	}
	return unique
}

func nulPathList(paths []string) []byte {
	size := len(paths)
	for _, path := range paths {
		size += len(path)
	}
	input := make([]byte, 0, size)
	for _, path := range paths {
		input = append(input, path...)
		input = append(input, 0)
	}
	return input
}

func boundedGitDiagnostic(value string) string {
	const limit = 256
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// CheckSubmodulesClean refuses a review snapshot when a checked-out
// submodule contains uncommitted or untracked work. A Git tree records only a
// submodule's commit ID; accepting a dirty submodule would therefore produce
// a fingerprint that silently omits files the reviewer can see on disk.
func (c *Client) CheckSubmodulesClean(ctx context.Context) error {
	root, err := worktreeEvidenceRoot(ctx, c.dir)
	if err != nil {
		return err
	}
	return checkSubmodulesCleanAtRoot(ctx, root)
}

func checkSubmodulesCleanAtRoot(ctx context.Context, root string) error {
	rooted := New(root)
	out, exceeded, err := rooted.runWithEnvOutputLimit(ctx, nil, maxGitIndexOutputBytes,
		worktreeEvidenceArgs("ls-files", "--stage", "-z", "--")...)
	if err != nil {
		return fmt.Errorf("inspect submodules: %w", err)
	}
	if exceeded {
		return fmt.Errorf("inspect submodules: Git index listing exceeds %d-byte review limit", maxGitIndexOutputBytes)
	}
	if records := bytes.Count(out, []byte{0}); records > maxGitIndexRecords {
		return fmt.Errorf("inspect submodules: Git index contains %d records; review limit is %d", records, maxGitIndexRecords)
	}
	records, err := nulFields(out)
	if err != nil {
		return fmt.Errorf("inspect submodules: %w", err)
	}
	seen := make(map[string]bool)
	for _, record := range records {
		metadata, path, ok := strings.Cut(record, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || path == "" {
			return fmt.Errorf("inspect submodules: malformed index record %q", record)
		}
		if fields[0] != "160000" {
			continue
		}
		if fields[2] != "0" {
			return fmt.Errorf("inspect submodules: %q has an unresolved index stage", path)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		if err := checkSubmoduleClean(ctx, root, path); err != nil {
			return err
		}
	}
	return nil
}

func checkSubmoduleClean(ctx context.Context, root, gitPath string) error {
	target := filepath.Join(root, filepath.FromSlash(gitPath))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("inspect submodules: path %q escapes repository root", gitPath)
	}
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("inspect submodules: submodule %q is unavailable: %w", gitPath, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("inspect submodules: submodule %q is not a checked-out directory", gitPath)
	}
	gitMarker, err := os.Lstat(filepath.Join(target, ".git"))
	if err != nil {
		return fmt.Errorf("inspect submodules: submodule %q is not initialized: %w", gitPath, err)
	}
	if gitMarker.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("inspect submodules: submodule %q has a symlinked .git marker", gitPath)
	}
	if !gitMarker.IsDir() && !gitMarker.Mode().IsRegular() {
		return fmt.Errorf("inspect submodules: submodule %q has an invalid .git marker", gitPath)
	}

	subRoot, err := worktreeEvidenceRoot(ctx, target)
	if err != nil {
		return fmt.Errorf("inspect submodules: submodule %q: %w", gitPath, err)
	}
	canonicalTarget, err := canonicalPath(target)
	if err != nil || subRoot != canonicalTarget {
		return fmt.Errorf("inspect submodules: submodule %q resolves to unexpected root %q", gitPath, subRoot)
	}
	if err := checkSubmodulesCleanAtRoot(ctx, subRoot); err != nil {
		return fmt.Errorf("inspect submodules: nested submodule in %q: %w", gitPath, err)
	}
	submodule := New(subRoot)
	paths, err := submodule.worktreeDiffPathInventory(ctx, nil)
	if err != nil {
		return fmt.Errorf("inspect submodules: inventory %q: %w", gitPath, err)
	}
	submoduleRoot, err := os.OpenRoot(subRoot)
	if err != nil {
		return fmt.Errorf("inspect submodules: open %q: %w", gitPath, err)
	}
	defer submoduleRoot.Close()
	if err := preflightWorktreeDiffPaths(ctx, submoduleRoot, paths, false); err != nil {
		return fmt.Errorf("inspect submodules: preflight %q: %w", gitPath, err)
	}
	if err := submodule.rejectWorktreeDiffFilters(ctx, nil, paths); err != nil {
		return fmt.Errorf("inspect submodules: %q: %w", gitPath, err)
	}
	status, exceeded, err := submodule.runWithEnvOutputLimit(ctx, nil, maxGitListOutputBytes,
		worktreeEvidenceArgs("status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none", "--no-renames")...)
	if err != nil {
		return fmt.Errorf("inspect submodules: status %q: %w", gitPath, err)
	}
	if exceeded {
		return fmt.Errorf("inspect submodules: status %q exceeds %d-byte review limit", gitPath, maxGitListOutputBytes)
	}
	if len(status) != 0 {
		return fmt.Errorf("dirty submodule %q contains unreviewed changes", gitPath)
	}
	return nil
}

// NumStat is one file's add/remove counts from `git diff --numstat`.
type NumStat struct {
	Path      string
	Additions int
	Removals  int
	Untracked bool
}

// DiffNumStat returns per-file addition/removal counts for the working tree
// vs base (empty base = index). Binary files and rename-only entries are
// reported with zero counts. Untracked files are not part of a diff; the
// caller handles them separately (ModifiedFiles in the orchestrator).
func (c *Client) DiffNumStat(ctx context.Context, base string) ([]NumStat, error) {
	args := []string{"diff", "--numstat", "-z"}
	if base != "" {
		args = append(args, base)
	}
	args = append(args, "--")
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("diff numstat: %w", err)
	}
	stats, err := parseNumStatZ(out)
	if err != nil {
		return nil, fmt.Errorf("diff numstat: %w", err)
	}
	return stats, nil
}

// Status returns the parsed status of the working tree.
func (c *Client) Status(ctx context.Context) (Status, error) {
	branch, err := c.CurrentBranch(ctx)
	if err != nil {
		return Status{}, err
	}
	out, err := c.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}
	entries, err := parseStatusZ(out)
	if err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}
	st := Status{Branch: branch, Dirty: len(entries) > 0, Files: entries}
	return st, nil
}

// Add stages the given paths. An empty paths list stages everything.
func (c *Client) Add(ctx context.Context, paths ...string) error {
	args := []string{"add"}
	if len(paths) == 0 {
		args = append(args, "-A")
	} else {
		args = append(args, "--")
		args = append(args, paths...)
	}
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("git add: %w", err)
	}
	return nil
}

// Commit creates a commit with the validated message. Validation follows the
// pipeline rules: a non-empty subject of at most 72 runes, no trailing
// whitespace, and a body separated from the subject by a blank line.
func (c *Client) Commit(ctx context.Context, message string) error {
	if err := validCommitMessage(message); err != nil {
		return err
	}
	if _, err := c.run(ctx, "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w", err)
	}
	return nil
}

// CommitOnly creates a commit containing exactly paths. Other entries that
// were already staged by the user remain staged and are never swept into the
// commit. Callers must stage paths first so new files and deletions are
// represented correctly.
func (c *Client) CommitOnly(ctx context.Context, message string, paths ...string) error {
	if err := validCommitMessage(message); err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("git commit only: at least one path is required")
	}
	args := []string{"commit", "--only", "-m", message, "--"}
	args = append(args, paths...)
	if _, err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("git commit only: %w", err)
	}
	return nil
}

// UnstageAll moves everything out of the index.
func (c *Client) UnstageAll(ctx context.Context) error {
	if _, err := c.run(ctx, "reset"); err != nil {
		return fmt.Errorf("git reset: %w", err)
	}
	return nil
}

// Discard reverts the working-tree changes of one file.
func (c *Client) Discard(ctx context.Context, path string) error {
	if _, err := c.run(ctx, "checkout", "--", path); err != nil {
		return fmt.Errorf("git checkout %s: %w", path, err)
	}
	return nil
}

// CommitHash returns the short hash of the HEAD commit.
func (c *Client) CommitHash(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("commit hash: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *Client) run(ctx context.Context, args ...string) ([]byte, error) {
	return c.runWithEnv(ctx, nil, args...)
}

func (c *Client) runWithEnv(ctx context.Context, overrides map[string]string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.dir
	c.configureEnvironment(cmd, overrides)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out, nil
}

// runWithEnvOutputLimit drains stdout while retaining at most limit bytes.
// The exceeded result is separate from command failure so semantic callers
// can refuse the entire result with a domain-specific error instead of ever
// mistaking a prefix for complete output.
func (c *Client) runWithEnvOutputLimit(ctx context.Context, overrides map[string]string, limit int, args ...string) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.dir
	c.configureEnvironment(cmd, overrides)
	stdout := &boundedCommandBuffer{limit: limit}
	stderr := &boundedCommandBuffer{limit: maxGitDiagnosticBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.exceeded {
		// The bounded writer deliberately stops the copy as soon as the first
		// byte beyond the ceiling arrives. The pipe closes, Git exits, and the
		// semantic caller turns this flag into its precise refusal message.
		return stdout.Bytes(), true, nil
	}
	if err != nil {
		msg := strings.TrimSpace(string(stderr.Bytes()))
		if stderr.exceeded {
			msg += " … diagnostic output limit exceeded"
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, stdout.exceeded, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), stdout.exceeded, nil
}

// runWithEnvNoOutput is for Git mutations whose stdout is not part of their
// contract (read-tree/add). The child writes stdout directly to the null
// device, while stderr remains bounded for a useful failure diagnostic.
func (c *Client) runWithEnvNoOutput(ctx context.Context, overrides map[string]string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.dir
	c.configureEnvironment(cmd, overrides)
	stderr := &boundedCommandBuffer{limit: maxGitDiagnosticBytes}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(string(stderr.Bytes()))
		if stderr.exceeded {
			msg += " … diagnostic output limit exceeded"
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

// runWithEnvInputOutputLimit is the bounded-output equivalent used by Git
// commands whose filename input must be NUL-delimited rather than placed in
// argv. The caller owns the semantic interpretation of an exceeded result.
func (c *Client) runWithEnvInputOutputLimit(ctx context.Context, overrides map[string]string, input []byte, limit int, args ...string) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.dir
	c.configureEnvironment(cmd, overrides)
	cmd.Stdin = bytes.NewReader(input)
	stdout := &boundedCommandBuffer{limit: limit}
	stderr := &boundedCommandBuffer{limit: maxGitDiagnosticBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if stdout.exceeded {
		return stdout.Bytes(), true, nil
	}
	if err != nil {
		msg := strings.TrimSpace(string(stderr.Bytes()))
		if stderr.exceeded {
			msg += " … diagnostic output limit exceeded"
		}
		if msg == "" {
			msg = err.Error()
		}
		return nil, false, fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), false, nil
}

func (c *Client) runWithEnvInputNoOutput(ctx context.Context, overrides map[string]string, input []byte, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.dir
	c.configureEnvironment(cmd, overrides)
	cmd.Stdin = bytes.NewReader(input)
	stderr := &boundedCommandBuffer{limit: maxGitDiagnosticBytes}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(string(stderr.Bytes()))
		if stderr.exceeded {
			msg += " … diagnostic output limit exceeded"
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return nil
}

func (c *Client) configureEnvironment(cmd *exec.Cmd, overrides map[string]string) {
	effectiveOverrides := overrides
	if c.ceiling != "" {
		effectiveOverrides = make(map[string]string, len(overrides)+1)
		for name, value := range overrides {
			effectiveOverrides[name] = value
		}
		effectiveOverrides["GIT_CEILING_DIRECTORIES"] = c.ceiling
	}
	if len(effectiveOverrides) > 0 {
		cmd.Env = mergeEnvironment(effectiveOverrides)
	}
}

type boundedCommandBuffer struct {
	buf      bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedCommandBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := max(b.limit-b.buf.Len(), 0)
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.exceeded = true
		return written, errGitCommandOutputLimit
	}
	_, _ = b.buf.Write(p)
	return written, nil
}

func (b *boundedCommandBuffer) Bytes() []byte {
	return append([]byte(nil), b.buf.Bytes()...)
}

func mergeEnvironment(overrides map[string]string) []string {
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

func parseNameStatusZ(out []byte) ([]FileChange, error) {
	fields, err := nulFields(out)
	if err != nil {
		return nil, err
	}
	var changes []FileChange
	for i := 0; i < len(fields); {
		status := fields[i]
		i++
		if status == "" {
			return nil, errors.New("empty change status")
		}
		if i >= len(fields) || fields[i] == "" {
			return nil, fmt.Errorf("missing path for change status %q", status)
		}
		fc := FileChange{Type: status, Path: fields[i]}
		i++
		if len(fc.Type) > 1 {
			fc.Type = fc.Type[:1] // strip the similarity score: "R100" → "R"
		}
		if fc.Type == "R" || fc.Type == "C" {
			if i >= len(fields) || fields[i] == "" {
				return nil, fmt.Errorf("missing destination path for change status %q", status)
			}
			fc.OldPath = fc.Path
			fc.Path = fields[i]
			i++
		}
		changes = append(changes, fc)
	}
	return changes, nil
}

func parseStatusZ(out []byte) ([]StatusEntry, error) {
	fields, err := nulFields(out)
	if err != nil {
		return nil, err
	}
	var entries []StatusEntry
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		if len(record) < 4 || record[2] != ' ' || record[3:] == "" {
			return nil, fmt.Errorf("malformed porcelain status record %q", record)
		}
		entry := StatusEntry{
			Path:       record[3:],
			IndexState: record[0],
			Worktree:   record[1],
		}
		if isRenameOrCopy(entry.IndexState) || isRenameOrCopy(entry.Worktree) {
			i++
			if i >= len(fields) || fields[i] == "" {
				return nil, fmt.Errorf("missing source path for renamed status entry %q", entry.Path)
			}
			// Porcelain v1 reverses rename fields with -z: destination first,
			// then source. Keep Path as the current destination.
			entry.OldPath = fields[i]
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func parseNumStatZ(out []byte) ([]NumStat, error) {
	fields, err := nulFields(out)
	if err != nil {
		return nil, err
	}
	var stats []NumStat
	for i := 0; i < len(fields); i++ {
		record := fields[i]
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("malformed numstat record %q", record)
		}
		adds, addErr := parseNumStatCount(parts[0])
		removals, removeErr := parseNumStatCount(parts[1])
		if addErr != nil || removeErr != nil {
			return nil, fmt.Errorf("malformed numstat counts %q, %q", parts[0], parts[1])
		}
		path := parts[2]
		if path == "" {
			// Rename/copy entries carry an empty path in the record followed by
			// source and destination as two independently NUL-terminated fields.
			if i+2 >= len(fields) || fields[i+1] == "" || fields[i+2] == "" {
				return nil, errors.New("malformed renamed numstat record")
			}
			path = fields[i+2]
			i += 2
		}
		stats = append(stats, NumStat{Path: path, Additions: adds, Removals: removals})
	}
	return stats, nil
}

func parseNumStatCount(value string) (int, error) {
	if value == "-" { // binary file
		return 0, nil
	}
	return strconv.Atoi(value)
}

func nulFields(out []byte) ([]string, error) {
	if len(out) == 0 {
		return nil, nil
	}
	if out[len(out)-1] != 0 {
		return nil, errors.New("NUL-delimited git output is not terminated")
	}
	raw := bytes.Split(out[:len(out)-1], []byte{0})
	fields := make([]string, len(raw))
	for i := range raw {
		fields[i] = string(raw[i])
	}
	return fields, nil
}

func isRenameOrCopy(state byte) bool {
	return state == 'R' || state == 'C'
}

func validBranchName(name string) error {
	if name == "" {
		return errors.New("branch name is required")
	}
	if strings.ContainsAny(name, " ~^:?*[\\ \t") || strings.HasSuffix(name, "/") || strings.HasPrefix(name, "-") || name == "@" {
		return fmt.Errorf("branch name %q is not a valid git ref", name)
	}
	return nil
}

func validCommitMessage(message string) error {
	if strings.TrimSpace(message) == "" {
		return errors.New("commit message is required")
	}
	lines := strings.Split(strings.TrimRight(message, "\n"), "\n")
	if utf8.RuneCountInString(strings.TrimRight(lines[0], " \t")) > 72 {
		return errors.New("commit subject exceeds 72 characters")
	}
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line != strings.TrimRight(line, " \t") {
			return errors.New("commit message lines must not have trailing whitespace")
		}
	}
	return nil
}
