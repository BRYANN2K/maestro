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
	"unicode/utf8"
)

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
	if err != nil {
		return "", fmt.Errorf("resolve repository root from %q: %w", dir, err)
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
	st, err := c.Status(ctx)
	if err != nil {
		return nil, fmt.Errorf("all changes status: %w", err)
	}
	for _, f := range st.Files {
		if f.Worktree != '?' && f.IndexState != '?' {
			continue
		}
		changes = append(changes, FileChange{Path: f.Path, Type: "A"})
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
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("diff name-status: %w", err)
	}
	changes, err := parseNameStatusZ(out)
	if err != nil {
		return nil, fmt.Errorf("diff name-status: %w", err)
	}
	return changes, nil
}

// UntrackedFiles lists untracked, non-ignored files in the working tree.
func (c *Client) UntrackedFiles(ctx context.Context) ([]string, error) {
	out, err := c.run(ctx, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("ls-files others: %w", err)
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
// index overlays the worktree on base, so the user's real index and working
// files remain untouched and partially staged state cannot hide evidence.
func (c *Client) WorktreeDiff(ctx context.Context, base string) (string, error) {
	if strings.TrimSpace(base) == "" {
		return "", errors.New("worktree diff: base revision is required")
	}
	root, err := c.RepositoryRoot(ctx)
	if err != nil {
		return "", fmt.Errorf("worktree diff: %w", err)
	}
	rooted := New(root)
	if err := rooted.CheckSubmodulesClean(ctx); err != nil {
		return "", fmt.Errorf("worktree diff: %w", err)
	}
	tempRoot, err := os.MkdirTemp("", "maestro-worktree-diff-")
	if err != nil {
		return "", fmt.Errorf("worktree diff: create private index: %w", err)
	}
	defer os.RemoveAll(tempRoot)

	env := map[string]string{"GIT_INDEX_FILE": filepath.Join(tempRoot, "index")}
	if _, err := rooted.runWithEnv(ctx, env, "read-tree", base); err != nil {
		return "", fmt.Errorf("worktree diff: seed private index: %w", err)
	}
	if _, err := rooted.runWithEnv(ctx, env, "add", "-A", "--", "."); err != nil {
		return "", fmt.Errorf("worktree diff: snapshot filesystem: %w", err)
	}
	out, err := rooted.runWithEnv(ctx, env, "diff", "--cached", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", base, "--")
	if err != nil {
		return "", fmt.Errorf("worktree diff: render patch: %w", err)
	}
	return string(out), nil
}

// CheckSubmodulesClean refuses a review snapshot when a checked-out
// submodule contains uncommitted or untracked work. A Git tree records only a
// submodule's commit ID; accepting a dirty submodule would therefore produce
// a fingerprint that silently omits files the reviewer can see on disk.
func (c *Client) CheckSubmodulesClean(ctx context.Context) error {
	root, err := c.RepositoryRoot(ctx)
	if err != nil {
		return err
	}
	rooted := New(root)
	out, err := rooted.run(ctx, "ls-files", "--stage", "-z", "--")
	if err != nil {
		return fmt.Errorf("inspect submodules: %w", err)
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

	subRoot, err := RepositoryRoot(ctx, target)
	if err != nil {
		return fmt.Errorf("inspect submodules: submodule %q: %w", gitPath, err)
	}
	canonicalTarget, err := canonicalPath(target)
	if err != nil || subRoot != canonicalTarget {
		return fmt.Errorf("inspect submodules: submodule %q resolves to unexpected root %q", gitPath, subRoot)
	}
	status, err := New(subRoot).run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("inspect submodules: status %q: %w", gitPath, err)
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
