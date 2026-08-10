package skills

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	stateVersion   = 1
	maxStateBytes  = 1 << 20
	stateLockStale = 2 * time.Minute
)

var safeStateComponent = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,191}$`)

// State is private per-project enablement. Session overrides take precedence.
type State struct {
	Version  int                        `json:"version"`
	Project  map[string]bool            `json:"project,omitempty"`
	Sessions map[string]map[string]bool `json:"sessions,omitempty"`
}

// StateStore persists a project's enablement atomically as a 0600 file.
type StateStore struct {
	dir        string
	anchor     string
	rel        string
	projectKey string
	initErr    error
	mu         sync.Mutex
}

func NewStateStore(dir, projectKey string) *StateStore {
	store := &StateStore{dir: dir, projectKey: normalizedStateComponent(projectKey)}
	if dir != "" {
		store.anchor, store.rel, store.dir, store.initErr = resolveStateAnchor(dir)
	}
	return store
}

func normalizedStateComponent(value string) string {
	if value == "" || safeStateComponent.MatchString(value) {
		return value
	}
	sum := sha256.Sum256([]byte(value))
	return "project-" + hex.EncodeToString(sum[:12])
}

func (s *StateStore) Load(ctx context.Context) (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(ctx)
}

func (s *StateStore) loadLocked(ctx context.Context) (State, error) {
	state := newState()
	if err := ctx.Err(); err != nil {
		return state, err
	}
	if s.initErr != nil {
		return state, s.initErr
	}
	if s.dir == "" || s.projectKey == "" {
		return state, nil
	}
	if !safeStateComponent.MatchString(s.projectKey) {
		return state, errors.New("skill state project key is unsafe")
	}
	root, err := s.openStateRoot(false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("open skill state: %w", err)
	}
	defer root.Close()
	name := s.projectKey + ".json"
	before, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("inspect skill state: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() > maxStateBytes {
		return state, errors.New("skill state must be a bounded regular file, not a symlink")
	}
	if err := validateStateFilePermissions(before); err != nil {
		return state, err
	}
	file, err := root.Open(name)
	if err != nil {
		return state, fmt.Errorf("open skill state: %w", err)
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return state, errors.New("skill state changed while opening")
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return state, fmt.Errorf("read skill state: %w", readErr)
	}
	if closeErr != nil || len(data) > maxStateBytes {
		return state, errors.New("skill state exceeds 1 MiB")
	}
	after, err := root.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() || !os.SameFile(opened, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return state, errors.New("skill state changed while reading")
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return newState(), fmt.Errorf("decode skill state: %w", err)
	}
	if state.Version != stateVersion {
		return newState(), fmt.Errorf("unsupported skill state version %d", state.Version)
	}
	if state.Project == nil {
		state.Project = map[string]bool{}
	}
	if state.Sessions == nil {
		state.Sessions = map[string]map[string]bool{}
	}
	return state, nil
}

func (s *StateStore) SetEnabled(ctx context.Context, id string, enabled bool, scope EnableScope, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initErr != nil {
		return s.initErr
	}
	if s.dir == "" || !safeStateComponent.MatchString(s.projectKey) {
		return errors.New("skill state path is unavailable")
	}
	if id == "" || len(id) > 256 {
		return errors.New("skill ID is invalid")
	}
	if scope == EnableSession && !safeStateComponent.MatchString(sessionID) {
		return errors.New("skill session ID is unsafe")
	}
	root, err := s.openStateRoot(true)
	if err != nil {
		return fmt.Errorf("open skill state directory: %w", err)
	}
	defer root.Close()
	unlock, err := acquireStateLock(ctx, root, s.projectKey+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	state, err := s.loadFromRootLocked(ctx, root)
	if err != nil {
		return err
	}
	if scope == EnableSession {
		if state.Sessions[sessionID] == nil {
			state.Sessions[sessionID] = map[string]bool{}
		}
		state.Sessions[sessionID][id] = enabled
	} else {
		state.Project[id] = enabled
	}
	return s.saveToRootLocked(root, state)
}

// loadFromRootLocked mirrors loadLocked while the cross-process mutation lock
// is held. Using a temporary Store rooted at the same directory would deadlock
// on this Store's in-process mutex, so the bounded read lives here explicitly.
func (s *StateStore) loadFromRootLocked(ctx context.Context, root *os.Root) (State, error) {
	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	state := newState()
	name := s.projectKey + ".json"
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() > maxStateBytes {
		return state, errors.New("skill state must be a bounded regular file, not a symlink")
	}
	if err := validateStateFilePermissions(before); err != nil {
		return state, err
	}
	file, err := root.Open(name)
	if err != nil {
		return state, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return state, errors.New("skill state changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	_ = file.Close()
	if err != nil || len(data) > maxStateBytes {
		return state, errors.New("skill state read failed or exceeded 1 MiB")
	}
	after, err := root.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return state, errors.New("skill state changed while reading")
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return newState(), fmt.Errorf("decode skill state: %w", err)
	}
	if state.Version != stateVersion {
		return newState(), fmt.Errorf("unsupported skill state version %d", state.Version)
	}
	if state.Project == nil {
		state.Project = map[string]bool{}
	}
	if state.Sessions == nil {
		state.Sessions = map[string]map[string]bool{}
	}
	return state, nil
}

func (s *StateStore) saveToRootLocked(root *os.Root, state State) error {
	state.Version = stateVersion
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode skill state: %w", err)
	}
	if len(data) > maxStateBytes {
		return errors.New("skill state exceeds 1 MiB")
	}
	token := make([]byte, 8)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("create skill state temporary name: %w", err)
	}
	tmp := ".skills-" + hex.EncodeToString(token) + ".tmp"
	file, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create skill state temporary file: %w", err)
	}
	cleanup := func() { _ = root.Remove(tmp) }
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("write skill state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("sync skill state: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return fmt.Errorf("secure skill state: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close skill state: %w", err)
	}
	name := s.projectKey + ".json"
	if info, err := root.Lstat(name); err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		cleanup()
		return errors.New("refusing to replace unsafe skill state target")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanup()
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		cleanup()
		return fmt.Errorf("publish skill state: %w", err)
	}
	if err := syncStateDirectory(root); err != nil {
		return fmt.Errorf("sync skill state directory: %w", err)
	}
	return nil
}

func newState() State {
	return State{Version: stateVersion, Project: map[string]bool{}, Sessions: map[string]map[string]bool{}}
}

// resolveStateAnchor chooses a pre-existing trusted boundary. User-home and
// OS-temp paths are common and may themselves contain platform-managed
// symlinks (notably /var on macOS), so they are accepted as anchors while every
// caller-controlled component below them is validated with Lstat.
func resolveStateAnchor(path string) (anchor, rel, absolute string, err error) {
	absolute, err = filepath.Abs(path)
	if err != nil {
		return "", "", "", fmt.Errorf("resolve skill state directory: %w", err)
	}
	absolute = filepath.Clean(absolute)
	var candidates []string
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		candidates = append(candidates, home)
	}
	candidates = append(candidates, os.TempDir())
	// /tmp is a conventional trusted temporary root on Unix. On Darwin it is
	// a platform-managed alias to /private/tmp while os.TempDir points deeper
	// into /var/folders. Treating /tmp as an ordinary path component rejects a
	// valid state directory before Maestro can display the Skills settings.
	// Only the trusted anchor itself is canonicalized; every component below it
	// remains subject to the Lstat/os.Root checks in openStateRoot.
	if filepath.Separator == '/' {
		candidates = append(candidates, string(filepath.Separator)+"tmp")
	}
	for _, candidate := range candidates {
		candidateAbs, absErr := filepath.Abs(candidate)
		if absErr == nil && filepath.Clean(candidateAbs) == absolute {
			return "", "", "", errors.New("skill state directory must be below, not equal to, a trusted anchor")
		}
	}
	if trustedAnchor, relative, canonicalPath, ok := resolveTrustedStateAnchor(absolute, candidates); ok {
		return trustedAnchor, relative, canonicalPath, nil
	}
	volume := filepath.VolumeName(absolute)
	anchor = volume + string(filepath.Separator)
	if anchor == "" {
		anchor = string(filepath.Separator)
	}
	rel, err = filepath.Rel(anchor, absolute)
	if err != nil || rel == "." {
		return "", "", "", errors.New("skill state directory must be below a trusted filesystem anchor")
	}
	return anchor, rel, absolute, nil
}

// resolveTrustedStateAnchor canonicalizes only a caller-independent boundary
// (home, the OS temp directory, or the conventional Unix /tmp root). This
// accepts platform aliases such as Darwin's /tmp -> /private/tmp without
// resolving any caller-controlled descendant. Descendant symlinks therefore
// remain visible to openStateRoot and are rejected fail-closed.
func resolveTrustedStateAnchor(path string, candidates []string) (anchor, rel, absolute string, ok bool) {
	for _, candidate := range candidates {
		candidateAbs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		candidateAbs = filepath.Clean(candidateAbs)
		if !pathWithin(candidateAbs, path) || candidateAbs == path {
			continue
		}
		relative, err := filepath.Rel(candidateAbs, path)
		if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		canonicalAnchor, err := filepath.EvalSymlinks(candidateAbs)
		if err != nil {
			continue
		}
		canonicalAnchor, err = filepath.Abs(canonicalAnchor)
		if err != nil {
			continue
		}
		canonicalAnchor = filepath.Clean(canonicalAnchor)
		info, err := os.Stat(canonicalAnchor)
		if err != nil || !info.IsDir() {
			continue
		}
		canonicalPath := filepath.Clean(filepath.Join(canonicalAnchor, relative))
		if !pathWithin(canonicalAnchor, canonicalPath) {
			continue
		}
		return canonicalAnchor, relative, canonicalPath, true
	}
	return "", "", "", false
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (s *StateStore) openStateRoot(create bool) (*os.Root, error) {
	base, err := os.OpenRoot(s.anchor)
	if err != nil {
		return nil, fmt.Errorf("open skill state anchor: %w", err)
	}
	defer base.Close()
	clean := filepath.Clean(s.rel)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return nil, errors.New("skill state directory is outside its trusted anchor")
	}
	current := ""
	var finalInfo os.FileInfo
	for _, component := range strings.Split(clean, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return nil, errors.New("skill state directory contains an unsafe component")
		}
		current = filepath.Join(current, component)
		info, statErr := base.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) && create {
			if mkdirErr := base.Mkdir(current, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				return nil, fmt.Errorf("create skill state directory: %w", mkdirErr)
			}
			info, statErr = base.Lstat(current)
		}
		if statErr != nil {
			return nil, statErr
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, errors.New("skill state path components must be real directories, not symlinks")
		}
		finalInfo = info
	}
	if !create {
		if err := validateStateDirectoryPermissions(finalInfo); err != nil {
			return nil, err
		}
	}
	if create {
		if err := base.Chmod(clean, 0o700); err != nil {
			return nil, fmt.Errorf("secure skill state directory: %w", err)
		}
	}
	child, err := base.OpenRoot(clean)
	if err != nil {
		return nil, fmt.Errorf("open skill state directory: %w", err)
	}
	opened, openErr := child.Stat(".")
	after, afterErr := base.Lstat(clean)
	if openErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !after.IsDir() ||
		finalInfo == nil || !os.SameFile(finalInfo, opened) || !os.SameFile(opened, after) {
		_ = child.Close()
		return nil, errors.New("skill state directory changed while opening")
	}
	return child, nil
}

func acquireStateLock(ctx context.Context, root *os.Root, name string) (func(), error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("create skill state lock token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)
	for {
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, writeErr := file.WriteString(token); writeErr != nil {
				_ = file.Close()
				_ = root.Remove(name)
				return nil, fmt.Errorf("record skill state lock owner: %w", writeErr)
			}
			if syncErr := file.Sync(); syncErr != nil {
				_ = file.Close()
				_ = root.Remove(name)
				return nil, fmt.Errorf("sync skill state lock: %w", syncErr)
			}
			_ = file.Close()
			return func() {
				owner, readErr := readStateLockOwner(root, name)
				if readErr == nil && owner == token {
					_ = root.Remove(name)
				}
			}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("acquire skill state lock: %w", err)
		}
		info, statErr := root.Lstat(name)
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect skill state lock: %w", statErr)
		}
		if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
			return nil, errors.New("skill state lock must be a regular file, not a symlink")
		}
		if statErr == nil && time.Since(info.ModTime()) > stateLockStale {
			quarantine := name + ".stale-" + token
			if renameErr := root.Rename(name, quarantine); renameErr == nil {
				_ = root.Remove(quarantine)
				continue
			}
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func readStateLockOwner(root *os.Root, name string) (string, error) {
	before, err := root.Lstat(name)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() > 64 {
		return "", errors.New("invalid skill state lock owner")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) || !opened.Mode().IsRegular() {
		_ = file.Close()
		return "", errors.New("skill state lock changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	_ = file.Close()
	if err != nil || len(data) > 64 {
		return "", errors.New("invalid skill state lock owner")
	}
	after, err := root.Lstat(name)
	if err != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, after) || before.Size() != after.Size() {
		return "", errors.New("skill state lock changed while reading")
	}
	return string(data), nil
}

// StatePath is exposed for tests and diagnostics without exposing mutable
// StateStore internals.
func (s *StateStore) StatePath() string {
	return filepath.Join(s.dir, s.projectKey+".json")
}
