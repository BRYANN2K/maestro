package git

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const checkpointSnapshotVersion = 2

// UntrackedFile is a binary-safe snapshot of an untracked regular file or
// symbolic link. Directories are not represented because Git itself does not
// track empty directories.
type UntrackedFile struct {
	Kind string      `json:"kind"` // file | symlink
	Data []byte      `json:"data,omitempty"`
	Link string      `json:"link,omitempty"`
	Mode os.FileMode `json:"mode,omitempty"`
}

// Checkpoint is an exact snapshot of the index, tracked working tree,
// untracked files, and session state. IndexTree and WorktreeTree deliberately
// use separate Git trees so partially staged changes survive a rewind.
type Checkpoint struct {
	ID              string                   `json:"id"`
	SnapshotVersion int                      `json:"snapshot_version,omitempty"`
	Head            string                   `json:"head,omitempty"`
	Worktree        string                   `json:"worktree,omitempty"`
	GitCommonDir    string                   `json:"git_common_dir,omitempty"`
	IndexTree       string                   `json:"index_tree,omitempty"`
	WorktreeTree    string                   `json:"worktree_tree,omitempty"`
	UntrackedFiles  map[string]UntrackedFile `json:"untracked_files,omitempty"`
	SpecRev         string                   `json:"spec_rev"`
	Conv            string                   `json:"conv"`
	Changed         []string                 `json:"changed"`
	Created         time.Time                `json:"created"`
	RecoveryFor     string                   `json:"recovery_for,omitempty"`

	// Code and Untracked only exist so old checkpoint files remain readable.
	// Version 1 code snapshots cannot be restored safely because their diff is
	// relative to HEAD and does not preserve the index.
	Code      string            `json:"code,omitempty"`
	Untracked map[string]string `json:"untracked,omitempty"`
}

// RewindResult identifies both the restored checkpoint and the durable
// checkpoint created immediately beforehand. The latter is intentionally
// retained so a user can undo a rewind.
type RewindResult struct {
	Checkpoint Checkpoint
	Recovery   Checkpoint
}

// CheckpointStore persists checkpoints per project.
type CheckpointStore struct {
	dir string
}

// NewCheckpointStore builds a store at dir.
func NewCheckpointStore(dir string) *CheckpointStore {
	return &CheckpointStore{dir: dir}
}

// Create snapshots the exact current index, tracked working tree, untracked
// files, and conversation. specRev is the current spec.md content hash.
func (s *CheckpointStore) Create(ctx context.Context, c *Client, convJSON string, specRev string) (Checkpoint, error) {
	return s.create(ctx, c, convJSON, specRev, "")
}

func (s *CheckpointStore) create(ctx context.Context, c *Client, convJSON, specRev, recoveryFor string) (Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return Checkpoint{}, err
	}
	id, err := newCheckpointID()
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint id: %w", err)
	}
	worktree, commonDir, err := repositoryIdentity(ctx, c)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint repository identity: %w", err)
	}
	if err := ensureStoreOutsideWorktree(s.dir, worktree); err != nil {
		return Checkpoint{}, err
	}
	if out, configErr := c.run(ctx, "config", "--bool", "core.sparseCheckout"); configErr == nil && strings.TrimSpace(string(out)) == "true" {
		return Checkpoint{}, errors.New("checkpoint: sparse checkouts are not supported safely")
	}
	if err := ensureSupportedGitState(ctx, c); err != nil {
		return Checkpoint{}, err
	}
	headOut, err := c.run(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint HEAD: %w", err)
	}
	head := strings.TrimSpace(string(headOut))
	indexOut, err := c.run(ctx, "write-tree")
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint index: %w", err)
	}
	indexTree := strings.TrimSpace(string(indexOut))
	worktreeTree, err := snapshotWorktreeTree(ctx, c, indexTree)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint worktree: %w", err)
	}
	untracked, err := snapshotUntracked(ctx, c)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint untracked files: %w", err)
	}
	changed, err := changedPaths(ctx, c, untracked)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint changed files: %w", err)
	}

	cp := Checkpoint{
		ID:              id,
		SnapshotVersion: checkpointSnapshotVersion,
		Head:            head,
		Worktree:        worktree,
		GitCommonDir:    commonDir,
		IndexTree:       indexTree,
		WorktreeTree:    worktreeTree,
		UntrackedFiles:  untracked,
		SpecRev:         specRev,
		Conv:            convJSON,
		Changed:         changed,
		Created:         time.Now().UTC(),
		RecoveryFor:     recoveryFor,
	}
	if err := protectSnapshot(ctx, c, cp); err != nil {
		return Checkpoint{}, fmt.Errorf("checkpoint protect snapshot: %w", err)
	}
	if err := s.save(cp); err != nil {
		unprotectSnapshot(context.Background(), c, cp)
		return Checkpoint{}, err
	}
	return cp, nil
}

func ensureSupportedGitState(ctx context.Context, c *Client) error {
	out, err := c.run(ctx, "status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return fmt.Errorf("checkpoint status: %w", err)
	}
	for _, record := range splitNUL(out) {
		if strings.HasPrefix(record, "u ") {
			return errors.New("checkpoint: an unmerged index cannot be snapshotted safely")
		}
		if !strings.HasPrefix(record, "1 ") && !strings.HasPrefix(record, "2 ") {
			continue
		}
		fields := strings.SplitN(record, " ", 4)
		if len(fields) >= 3 && strings.HasPrefix(fields[2], "S") {
			return errors.New("checkpoint: changed or dirty submodules are not supported safely")
		}
	}
	return nil
}

func repositoryIdentity(ctx context.Context, c *Client) (worktree, commonDir string, err error) {
	worktreeOut, err := c.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", err
	}
	worktree, err = canonicalExistingPath(strings.TrimSpace(string(worktreeOut)))
	if err != nil {
		return "", "", err
	}
	commonOut, err := c.run(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", "", err
	}
	commonPath := strings.TrimSpace(string(commonOut))
	if !filepath.IsAbs(commonPath) {
		commonPath = filepath.Join(c.dir, commonPath)
	}
	commonDir, err = canonicalExistingPath(commonPath)
	if err != nil {
		return "", "", err
	}
	return worktree, commonDir, nil
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

func ensureStoreOutsideWorktree(storeDir, worktree string) error {
	abs, err := filepath.Abs(storeDir)
	if err != nil {
		return fmt.Errorf("checkpoint store: %w", err)
	}
	resolved := filepath.Clean(abs)
	if evaluated, evalErr := filepath.EvalSymlinks(resolved); evalErr == nil {
		resolved = evaluated
	} else if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(resolved)); parentErr == nil {
		resolved = filepath.Join(parent, filepath.Base(resolved))
	}
	rel, err := filepath.Rel(worktree, resolved)
	if err != nil {
		return fmt.Errorf("checkpoint store: %w", err)
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return errors.New("checkpoint store must be outside the Git worktree")
	}
	return nil
}

func newCheckpointID() (string, error) {
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return fmt.Sprintf("cp-%d-%s", time.Now().UnixNano(), hex.EncodeToString(random[:])), nil
}

func snapshotWorktreeTree(ctx context.Context, c *Client, indexTree string) (string, error) {
	tempDir, err := os.MkdirTemp("", "maestro-checkpoint-index-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)
	indexPath := filepath.Join(tempDir, "index")
	if _, err := c.runWithIndex(ctx, indexPath, "read-tree", indexTree); err != nil {
		return "", err
	}
	// -u updates only paths already in the captured index. Consequently,
	// untracked files stay out of this tree and are snapshotted separately.
	if _, err := c.runWithIndex(ctx, indexPath, "add", "-u", "--", ":/"); err != nil {
		return "", err
	}
	out, err := c.runWithIndex(ctx, indexPath, "write-tree")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func snapshotUntracked(ctx context.Context, c *Client) (map[string]UntrackedFile, error) {
	paths, err := untrackedPaths(ctx, c)
	if err != nil {
		return nil, err
	}
	files := make(map[string]UntrackedFile, len(paths))
	for _, rel := range paths {
		full, err := safeWorktreePath(c.dir, rel)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(full)
		if err != nil {
			return nil, fmt.Errorf("snapshot %q: %w", rel, err)
		}
		switch {
		case info.Mode().IsRegular():
			data, err := os.ReadFile(full)
			if err != nil {
				return nil, fmt.Errorf("snapshot %q: %w", rel, err)
			}
			files[rel] = UntrackedFile{Kind: "file", Data: data, Mode: info.Mode().Perm()}
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(full)
			if err != nil {
				return nil, fmt.Errorf("snapshot symlink %q: %w", rel, err)
			}
			files[rel] = UntrackedFile{Kind: "symlink", Link: link}
		default:
			return nil, fmt.Errorf("snapshot %q: unsupported untracked file mode %s", rel, info.Mode())
		}
	}
	return files, nil
}

func changedPaths(ctx context.Context, c *Client, untracked map[string]UntrackedFile) ([]string, error) {
	out, err := c.run(ctx, "diff", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(untracked))
	for _, path := range splitNUL(out) {
		set[path] = struct{}{}
	}
	for path := range untracked {
		set[path] = struct{}{}
	}
	paths := make([]string, 0, len(set))
	for path := range set {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func protectSnapshot(ctx context.Context, c *Client, cp Checkpoint) error {
	refs := snapshotRefs(cp.ID)
	if _, err := c.run(ctx, "update-ref", refs[0], cp.IndexTree); err != nil {
		return err
	}
	if _, err := c.run(ctx, "update-ref", refs[1], cp.WorktreeTree); err != nil {
		_, _ = c.run(context.Background(), "update-ref", "-d", refs[0])
		return err
	}
	return nil
}

func unprotectSnapshot(ctx context.Context, c *Client, cp Checkpoint) {
	for _, ref := range snapshotRefs(cp.ID) {
		_, _ = c.run(ctx, "update-ref", "-d", ref)
	}
}

func snapshotRefs(id string) [2]string {
	return [2]string{
		"refs/maestro/checkpoints/" + id + "/index",
		"refs/maestro/checkpoints/" + id + "/worktree",
	}
}

func (s *CheckpointStore) save(cp Checkpoint) (retErr error) {
	if err := validCheckpointID(cp.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if retErr != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := io.Copy(tmp, bytes.NewReader(data)); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(s.dir, cp.ID+".json"))
}

// List returns the checkpoints, newest first.
func (s *CheckpointStore) List(ctx context.Context) ([]Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Checkpoint
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		cp, err := s.load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

// ListBySpec filters the checkpoints of one spec revision.
func (s *CheckpointStore) ListBySpec(ctx context.Context, specRev string) ([]Checkpoint, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Checkpoint
	for _, cp := range all {
		if cp.SpecRev == specRev {
			out = append(out, cp)
		}
	}
	return out, nil
}

func (s *CheckpointStore) load(name string) (Checkpoint, error) {
	if filepath.Base(name) != name || !strings.HasSuffix(name, ".json") {
		return Checkpoint{}, errors.New("invalid checkpoint filename")
	}
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return Checkpoint{}, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return Checkpoint{}, err
	}
	if err := validCheckpointID(cp.ID); err != nil {
		return Checkpoint{}, err
	}
	return cp, nil
}

// Load reads one checkpoint by ID.
func (s *CheckpointStore) Load(ctx context.Context, id string) (Checkpoint, error) {
	if err := ctx.Err(); err != nil {
		return Checkpoint{}, err
	}
	if err := validCheckpointID(id); err != nil {
		return Checkpoint{}, err
	}
	cp, err := s.load(id + ".json")
	if err != nil {
		return Checkpoint{}, err
	}
	if cp.ID != id {
		return Checkpoint{}, errors.New("checkpoint file id does not match requested id")
	}
	return cp, nil
}

// Rewind creates a durable recovery checkpoint, then restores the requested
// code snapshot. currentConv is persisted in the recovery checkpoint even for
// conversation-only rewinds. The caller owns validation and persistence of
// the target conversation JSON.
func (s *CheckpointStore) Rewind(ctx context.Context, c *Client, id string, code bool, currentConv, currentSpecRev string) (RewindResult, error) {
	target, err := s.Load(ctx, id)
	if err != nil {
		return RewindResult{}, err
	}
	if code {
		if err := validateSnapshot(ctx, c, target); err != nil {
			return RewindResult{}, fmt.Errorf("rewind code: %w", err)
		}
	}
	recovery, err := s.create(ctx, c, currentConv, currentSpecRev, id)
	if err != nil {
		return RewindResult{}, fmt.Errorf("rewind recovery checkpoint: %w", err)
	}
	result := RewindResult{Checkpoint: target, Recovery: recovery}
	if !code {
		return result, nil
	}
	if err := s.restoreCode(ctx, c, target); err != nil {
		rollbackErr := s.restoreCode(context.Background(), c, recovery)
		if rollbackErr != nil {
			return result, fmt.Errorf("rewind code: %w; automatic rollback to recovery checkpoint %s also failed: %v", err, recovery.ID, rollbackErr)
		}
		return result, fmt.Errorf("rewind code: %w; original state restored from recovery checkpoint %s", err, recovery.ID)
	}
	return result, nil
}

// RestoreCode restores a previously validated v2 snapshot without creating a
// second recovery checkpoint. It is intended only for transaction rollback
// after a later operation (for example session persistence) fails.
func (s *CheckpointStore) RestoreCode(ctx context.Context, c *Client, id string) error {
	cp, err := s.Load(ctx, id)
	if err != nil {
		return err
	}
	if err := validateSnapshot(ctx, c, cp); err != nil {
		return err
	}
	return s.restoreCode(ctx, c, cp)
}

func validateSnapshot(ctx context.Context, c *Client, cp Checkpoint) error {
	if cp.SnapshotVersion != checkpointSnapshotVersion {
		return fmt.Errorf("checkpoint %s uses legacy snapshot version %d and cannot safely restore code", cp.ID, cp.SnapshotVersion)
	}
	if !validObjectID(cp.Head) || !validObjectID(cp.IndexTree) || !validObjectID(cp.WorktreeTree) {
		return fmt.Errorf("checkpoint %s contains invalid Git object IDs", cp.ID)
	}
	worktree, commonDir, err := repositoryIdentity(ctx, c)
	if err != nil {
		return err
	}
	if worktree != cp.Worktree || commonDir != cp.GitCommonDir {
		return fmt.Errorf("checkpoint %s belongs to a different repository or worktree", cp.ID)
	}
	headOut, err := c.run(ctx, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return err
	}
	currentHead := strings.TrimSpace(string(headOut))
	if currentHead != cp.Head {
		return fmt.Errorf("checkpoint HEAD %s differs from current HEAD %s", cp.Head, currentHead)
	}
	for _, tree := range []string{cp.IndexTree, cp.WorktreeTree} {
		if _, err := c.run(ctx, "cat-file", "-e", tree+"^{tree}"); err != nil {
			return fmt.Errorf("checkpoint tree %s is unavailable: %w", tree, err)
		}
	}
	for rel, file := range cp.UntrackedFiles {
		if _, err := safeWorktreePath(c.dir, rel); err != nil {
			return err
		}
		if file.Kind != "file" && file.Kind != "symlink" {
			return fmt.Errorf("checkpoint untracked file %q has invalid kind %q", rel, file.Kind)
		}
		if file.Kind == "file" && file.Mode&^os.ModePerm != 0 {
			return fmt.Errorf("checkpoint untracked file %q has invalid mode %v", rel, file.Mode)
		}
	}
	return nil
}

func validObjectID(id string) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *CheckpointStore) restoreCode(ctx context.Context, c *Client, cp Checkpoint) error {
	if _, err := c.run(ctx, "read-tree", cp.IndexTree); err != nil {
		return fmt.Errorf("restore index: %w", err)
	}
	if _, err := c.run(ctx, "restore", "--source="+cp.WorktreeTree, "--worktree", "--", ":/"); err != nil {
		return fmt.Errorf("restore tracked worktree: %w", err)
	}
	current, err := untrackedPaths(ctx, c)
	if err != nil {
		return fmt.Errorf("list untracked files: %w", err)
	}
	for _, rel := range current {
		if _, keep := cp.UntrackedFiles[rel]; keep {
			continue
		}
		full, err := safeWorktreePath(c.dir, rel)
		if err != nil {
			return err
		}
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove post-checkpoint untracked file %q: %w", rel, err)
		}
	}
	paths := make([]string, 0, len(cp.UntrackedFiles))
	for rel := range cp.UntrackedFiles {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	for _, rel := range paths {
		if err := restoreUntracked(c.dir, rel, cp.UntrackedFiles[rel]); err != nil {
			return err
		}
	}
	return verifyRestoredSnapshot(ctx, c, cp)
}

func verifyRestoredSnapshot(ctx context.Context, c *Client, cp Checkpoint) error {
	indexOut, err := c.run(ctx, "write-tree")
	if err != nil {
		return fmt.Errorf("verify restored index: %w", err)
	}
	if strings.TrimSpace(string(indexOut)) != cp.IndexTree {
		return errors.New("verify restored index: tree does not match checkpoint")
	}
	worktreeTree, err := snapshotWorktreeTree(ctx, c, cp.IndexTree)
	if err != nil {
		return fmt.Errorf("verify restored worktree: %w", err)
	}
	if worktreeTree != cp.WorktreeTree {
		return errors.New("verify restored worktree: tree does not match checkpoint")
	}
	untracked, err := snapshotUntracked(ctx, c)
	if err != nil {
		return fmt.Errorf("verify restored untracked files: %w", err)
	}
	if !equalUntracked(untracked, cp.UntrackedFiles) {
		return errors.New("verify restored untracked files: state does not match checkpoint")
	}
	return nil
}

func equalUntracked(left, right map[string]UntrackedFile) bool {
	if len(left) != len(right) {
		return false
	}
	for path, snapshot := range left {
		other, ok := right[path]
		if !ok || snapshot.Kind != other.Kind || snapshot.Link != other.Link || snapshot.Mode != other.Mode || !bytes.Equal(snapshot.Data, other.Data) {
			return false
		}
	}
	return true
}

func restoreUntracked(root, rel string, snapshot UntrackedFile) error {
	full, err := safeWorktreePath(root, rel)
	if err != nil {
		return err
	}
	if err := ensureSafeParents(root, rel); err != nil {
		return err
	}
	if snapshot.Kind == "symlink" {
		if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("replace untracked symlink %q: %w", rel, err)
		}
		if err := os.Symlink(snapshot.Link, full); err != nil {
			return fmt.Errorf("restore untracked symlink %q: %w", rel, err)
		}
		return nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".maestro-rewind-*")
	if err != nil {
		return fmt.Errorf("restore untracked file %q: %w", rel, err)
	}
	tmpName := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(snapshot.Mode.Perm()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restore untracked file %q: %w", rel, err)
	}
	if _, err := io.Copy(tmp, bytes.NewReader(snapshot.Data)); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restore untracked file %q: %w", rel, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restore untracked file %q: %w", rel, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("restore untracked file %q: %w", rel, err)
	}
	if _, err := os.Lstat(full); err == nil {
		// os.Remove also handles an empty directory that replaced the captured
		// file; it never recursively deletes unknown or ignored contents.
		if err := os.Remove(full); err != nil {
			return fmt.Errorf("restore untracked file %q: %w", rel, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restore untracked file %q: %w", rel, err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		return fmt.Errorf("restore untracked file %q: %w", rel, err)
	}
	removeTemp = false
	return nil
}

func ensureSafeParents(root, rel string) error {
	parts := strings.Split(pathpkg.Dir(rel), "/")
	current := root
	for _, part := range parts {
		if part == "." || part == "" {
			continue
		}
		current = filepath.Join(current, filepath.FromSlash(part))
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("create checkpoint parent %q: %w", current, err)
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("checkpoint parent %q is not a safe directory", current)
		}
	}
	return nil
}

func untrackedPaths(ctx context.Context, c *Client) ([]string, error) {
	out, err := c.run(ctx, "ls-files", "--others", "--exclude-standard", "-z", "--", ":/")
	if err != nil {
		return nil, err
	}
	paths := splitNUL(out)
	for _, rel := range paths {
		if _, err := safeWorktreePath(c.dir, rel); err != nil {
			return nil, err
		}
	}
	return paths, nil
}

func splitNUL(data []byte) []string {
	fields := bytes.Split(data, []byte{0})
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 0 {
			paths = append(paths, string(field))
		}
	}
	return paths
}

func safeWorktreePath(root, rel string) (string, error) {
	if rel == "" || !utf8.ValidString(rel) || strings.ContainsRune(rel, 0) || filepath.IsAbs(rel) || pathpkg.IsAbs(rel) {
		return "", fmt.Errorf("unsafe checkpoint path %q", rel)
	}
	clean := pathpkg.Clean(rel)
	if clean != rel || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("unsafe checkpoint path %q", rel)
	}
	first := strings.SplitN(clean, "/", 2)[0]
	if strings.EqualFold(first, ".git") {
		return "", fmt.Errorf("unsafe checkpoint path %q", rel)
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	relCheck, err := filepath.Rel(root, full)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe checkpoint path %q", rel)
	}
	return full, nil
}

func validCheckpointID(id string) error {
	if len(id) < 4 || len(id) > 128 || !strings.HasPrefix(id, "cp-") {
		return errors.New("invalid checkpoint id")
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return errors.New("invalid checkpoint id")
		}
	}
	return nil
}

func (c *Client) runWithIndex(ctx context.Context, indexPath string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.dir
	cmd.Env = append(withoutEnv(os.Environ(), "GIT_INDEX_FILE"), "GIT_INDEX_FILE="+indexPath)
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

func withoutEnv(env []string, name string) []string {
	prefix := name + "="
	out := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			out = append(out, item)
		}
	}
	return out
}

// ApplyPatch applies a unified diff, optionally reversed.
func (c *Client) ApplyPatch(ctx context.Context, patch string, reverse bool) error {
	args := []string{"apply"}
	if reverse {
		args = append(args, "--reverse")
	}
	args = append(args, "-")
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.dir
	cmd.Stdin = strings.NewReader(patch)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git apply: %v: %s", err, out)
	}
	return nil
}

// SpecRev computes the revision hash of a spec file.
func SpecRev(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:12])
}
