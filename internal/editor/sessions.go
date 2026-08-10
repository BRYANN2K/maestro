package editor

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxEditorStateBytes int64 = 32 << 20

// SessionState is one detachable editor session: buffers + cursor + mode.
type SessionState struct {
	Project string        `json:"project"`
	Buffers []BufferState `json:"buffers"`
	CurBuf  int           `json:"cur_buf"`
}

// BufferState is one buffer's serializable state.
type BufferState struct {
	Path  string   `json:"path"`
	Lines []string `json:"lines"`
	Line  int      `json:"line"`
	Col   int      `json:"col"`
	Dirty bool     `json:"dirty"`
}

// SessionStore persists editor sessions for detach/attach (§5.2.3).
type SessionStore struct {
	dir string
}

// NewSessionStore builds a session store at dir.
func NewSessionStore(dir string) *SessionStore {
	return &SessionStore{dir: dir}
}

// SetDir switches the store root.
func (s *SessionStore) SetDir(dir string) { s.dir = dir }

// Save writes the editor state; returns the session file path.
func (s *SessionStore) Save(e *Editor) (string, error) {
	if s.dir == "" {
		return "", fmt.Errorf("session store dir not set")
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return "", err
	}
	state := SessionState{Project: e.Project}
	activeRestored := false
	for i, b := range e.Buffers {
		if _, ok := safeBufferLines(b.Lines); !ok {
			continue
		}
		if i == e.CurBuf {
			state.CurBuf = len(state.Buffers)
			activeRestored = true
		}
		state.Buffers = append(state.Buffers, BufferState{
			Path:  b.Path,
			Lines: append([]string(nil), b.Lines...),
			Line:  b.Cur.Line,
			Col:   b.Cur.Col,
			Dirty: b.Dirty,
		})
	}
	if !activeRestored {
		state.CurBuf = 0
	}
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxEditorStateBytes {
		return "", fmt.Errorf("editor session exceeds the 32 MiB state limit")
	}
	path := filepath.Join(s.dir, "editor-session.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// Load restores the editor state. Returns true when a session existed.
func (s *SessionStore) Load(e *Editor) (bool, error) {
	if s.dir == "" {
		return false, nil
	}
	path := filepath.Join(s.dir, "editor-session.json")
	data, err := readEditorState(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, err
	}
	e.Buffers = nil
	restoredActive := -1
	skipped := 0
	for i, bs := range state.Buffers {
		b, ok := restoreBufferState(e.Project, bs)
		if !ok {
			skipped++
			continue
		}
		b.Cur = Cursor{Line: bs.Line, Col: bs.Col}
		b.clamp()
		b.Dirty = bs.Dirty
		if i == state.CurBuf {
			restoredActive = len(e.Buffers)
		}
		e.Buffers = append(e.Buffers, b)
	}
	e.CurBuf = restoredActive
	if len(e.Buffers) == 0 {
		e.Buffers = append(e.Buffers, NewBuffer("", nil))
		e.CurBuf = 0
	} else if e.CurBuf < 0 || e.CurBuf >= len(e.Buffers) {
		e.CurBuf = 0
	}
	if skipped > 0 {
		e.Status = fmt.Sprintf("ignored %d unsafe restored buffer(s)", skipped)
	}
	return true, nil
}

// Clear removes the stored session.
func (s *SessionStore) Clear() {
	if s.dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(s.dir, "editor-session.json"))
}

// CrashStore persists dirty buffers at exit for atomic crash recovery
// (§5.2.3): restored on the next launch.
type CrashStore struct {
	dir string
}

// NewCrashStore builds a crash store at dir.
func NewCrashStore(dir string) *CrashStore { return &CrashStore{dir: dir} }

// SetDir switches the store root.
func (c *CrashStore) SetDir(dir string) { c.dir = dir }

// Save records the dirty buffers.
func (c *CrashStore) Save(e *Editor) error {
	if c.dir == "" {
		return nil
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	data := []byte(crashJSON(e))
	if int64(len(data)) > maxEditorStateBytes {
		return fmt.Errorf("editor crash state exceeds the 32 MiB state limit")
	}
	path := filepath.Join(c.dir, "crash.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	return nil
}

func crashJSON(e *Editor) string {
	state := SessionState{Project: e.Project, CurBuf: e.CurBuf}
	for _, b := range e.Buffers {
		if !b.Dirty {
			continue
		}
		if _, ok := safeBufferLines(b.Lines); !ok {
			continue
		}
		state.Buffers = append(state.Buffers, BufferState{
			Path: b.Path, Lines: append([]string(nil), b.Lines...),
			Line: b.Cur.Line, Col: b.Cur.Col, Dirty: true,
		})
	}
	data, _ := json.Marshal(state)
	return string(data)
}

// Restore recovers dirty buffers; returns them for the user to re-open.
func (c *CrashStore) Restore() ([]BufferState, error) {
	if c.dir == "" {
		return nil, nil
	}
	data, err := readEditorState(filepath.Join(c.dir, "crash.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state SessionState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	_ = os.Remove(filepath.Join(c.dir, "crash.json"))
	safe := make([]BufferState, 0, len(state.Buffers))
	for _, bs := range state.Buffers {
		if _, ok := restoreBufferState(state.Project, bs); ok {
			safe = append(safe, bs)
		}
	}
	return safe, nil
}

// RestoreBuffers loads the recovered states into the editor.
func (e *Editor) RestoreBuffers(states []BufferState) {
	skipped := 0
	restored := 0
	for _, bs := range states {
		b, ok := restoreBufferState(e.Project, bs)
		if !ok {
			skipped++
			continue
		}
		b.Cur = Cursor{Line: bs.Line, Col: bs.Col}
		b.clamp()
		b.Dirty = true
		e.Buffers = append(e.Buffers, b)
		restored++
	}
	if restored > 0 {
		e.CurBuf = len(e.Buffers) - 1
		e.Status = fmt.Sprintf("recovered %d dirty buffer(s)", restored)
	}
	if skipped > 0 {
		e.Status = fmt.Sprintf("ignored %d unsafe recovered buffer(s)", skipped)
	}
}

func readEditorState(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("editor state is not a regular file")
	}
	if info.Size() > maxEditorStateBytes {
		return nil, fmt.Errorf("editor state exceeds the 32 MiB limit")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxEditorStateBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxEditorStateBytes {
		return nil, fmt.Errorf("editor state exceeds the 32 MiB limit")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("editor state is not valid UTF-8")
	}
	return data, nil
}

func safeBufferLines(lines []string) ([]byte, bool) {
	total := int64(1) // Buffer.String always appends one final newline.
	for i, line := range lines {
		total += int64(len(line))
		if i > 0 {
			total++
		}
		if total > MaxEditableFileSize {
			return nil, false
		}
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	if reason := classifyFileText(data, true); reason != "" {
		return nil, false
	}
	data = []byte(strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n"))
	return data, true
}

// restoreBufferState validates both the stored text and, when present, the
// current on-disk file. This prevents a legacy session containing a binary
// executable from becoming an editable text buffer after restart.
func restoreBufferState(project string, bs BufferState) (*Buffer, bool) {
	data, ok := safeBufferLines(bs.Lines)
	if !ok {
		return nil, false
	}
	if bs.Path != "" && bs.Path != "untitled" {
		diskPath := bs.Path
		if !filepath.IsAbs(diskPath) && project != "" {
			diskPath = filepath.Join(project, diskPath)
		}
		if _, err := os.Stat(diskPath); err == nil {
			if _, err := Load(diskPath); err != nil {
				return nil, false
			}
		} else if !os.IsNotExist(err) {
			return nil, false
		}
	}
	return NewBuffer(bs.Path, data), true
}
