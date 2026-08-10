package editor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacySessionCannotRestoreBinaryAsEditableText(t *testing.T) {
	project := t.TempDir()
	stateDir := t.TempDir()
	binaryPath := filepath.Join(project, "tui.test")
	safePath := filepath.Join(project, "notes.txt")
	binary := []byte{0x7f, 'E', 'L', 'F', 0, 1, 2, 3}
	if err := os.WriteFile(binaryPath, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(safePath, []byte("safe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacy := SessionState{
		Project: project,
		CurBuf:  0,
		Buffers: []BufferState{
			{Path: binaryPath, Lines: []string{"File not opened", "old placeholder"}},
			{Path: safePath, Lines: []string{"safe"}},
			{Path: filepath.Join(project, "missing.txt"), Lines: []string{"unsafe\x1b[2J"}, Dirty: true},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "editor-session.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	e := NewEditor(project)
	store := NewSessionStore(stateDir)
	loaded, err := store.Load(e)
	if err != nil || !loaded {
		t.Fatalf("Load session = %v, %v", loaded, err)
	}
	if len(e.Buffers) != 1 || e.Buffer().Path != safePath || e.Buffer().String() != "safe\n" {
		t.Fatalf("restored buffers = %#v, active=%+v", e.Buffers, e.Buffer())
	}
	if !strings.Contains(e.Status, "ignored 2 unsafe") {
		t.Fatalf("status = %q", e.Status)
	}
	for _, buffer := range e.Buffers {
		if buffer.Path == binaryPath {
			t.Fatal("legacy binary session became an editor buffer")
		}
	}
	got, err := os.ReadFile(binaryPath)
	if err != nil || string(got) != string(binary) {
		t.Fatalf("binary changed during restore: %x, %v", got, err)
	}
}

func TestCrashRestoreDropsUnsafeLegacyBuffers(t *testing.T) {
	project := t.TempDir()
	binaryPath := filepath.Join(project, "tui.test")
	if err := os.WriteFile(binaryPath, []byte("MZprintable legacy placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}
	e := NewEditor(project)
	e.Buffers = []*Buffer{NewBuffer("safe.txt", []byte("safe\n"))}
	e.RestoreBuffers([]BufferState{
		{Path: binaryPath, Lines: []string{"apparently safe placeholder"}, Dirty: true},
		{Path: "missing.txt", Lines: []string{"bad\x00state"}, Dirty: true},
	})
	if len(e.Buffers) != 1 || e.Buffer().Path != "safe.txt" {
		t.Fatalf("unsafe crash buffers restored: %#v", e.Buffers)
	}
	if !strings.Contains(e.Status, "ignored 2 unsafe") {
		t.Fatalf("status = %q", e.Status)
	}
}

func TestSessionReaderRejectsInvalidUTF8AndOversizedState(t *testing.T) {
	stateDir := t.TempDir()
	path := filepath.Join(stateDir, "editor-session.json")
	store := NewSessionStore(stateDir)
	e := NewEditor(t.TempDir())

	if err := os.WriteFile(path, []byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(e); err == nil || loaded {
		t.Fatalf("invalid UTF-8 state = (%v, %v), want rejection", loaded, err)
	}

	if err := os.Truncate(path, maxEditorStateBytes+1); err != nil {
		t.Fatal(err)
	}
	if loaded, err := store.Load(e); err == nil || loaded {
		t.Fatalf("oversized state = (%v, %v), want rejection", loaded, err)
	}
}
