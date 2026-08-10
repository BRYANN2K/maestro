package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/editor"
	"github.com/bryann2k/maestro/internal/git"
)

func TestIDEBinaryOpenIsRejectedAcrossEveryNavigationRoute(t *testing.T) {
	m, project := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	safePath := filepath.Join(project, "safe.txt")
	binaryPath := filepath.Join(project, "tui.test")
	binary := []byte{0xcf, 0xfa, 0xed, 0xfe, 0, 1, 2, 3, 0x1b, '[', '2', 'J'}
	if err := os.WriteFile(safePath, []byte("active text\nsecond line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, binary, 0o755); err != nil {
		t.Fatal(err)
	}
	m.ToggleIDE()
	if !m.ide.OpenFileAt("safe.txt") {
		t.Fatal("failed to open text fixture")
	}
	active := m.ide.Ed.Buffer()
	active.Cur = editor.Cursor{Line: 0, Col: 1}
	m.ide.Ed.BeginSelectionAt(editor.Cursor{Line: 0, Col: 0})
	m.ide.Ed.UpdateSelectionCursor(editor.Cursor{Line: 0, Col: 1})
	m.pendingSelection = &selectionContext{Source: "ide", Path: safePath, Text: "a"}

	assertRejected := func(route string, open func()) {
		t.Helper()
		beforeText := active.String()
		beforeBuffers := len(m.ide.Ed.Buffers)
		beforeCursor := active.Cur
		open()
		if m.ide.Ed.Buffer() != active || len(m.ide.Ed.Buffers) != beforeBuffers || active.String() != beforeText || active.Cur != beforeCursor {
			t.Fatalf("%s changed active state: buffer=%p count=%d text=%q cursor=%+v", route, m.ide.Ed.Buffer(), len(m.ide.Ed.Buffers), active.String(), active.Cur)
		}
		for _, buffer := range m.ide.Ed.Buffers {
			if buffer.Path == binaryPath {
				t.Fatalf("%s injected the binary into an editor buffer", route)
			}
		}
		got, err := os.ReadFile(binaryPath)
		if err != nil || string(got) != string(binary) {
			t.Fatalf("%s changed binary: %x, %v", route, got, err)
		}
		if !strings.HasPrefix(m.ide.Ed.Status, "File unavailable:") {
			t.Fatalf("%s status = %q", route, m.ide.Ed.Status)
		}
		if len(m.status.toasts) == 0 || m.status.toasts[len(m.status.toasts)-1].Msg != m.ide.Ed.Status {
			t.Fatalf("%s did not expose a safe toast: %#v", route, m.status.toasts)
		}
		if !utf8.ValidString(m.ide.Ed.Status) || strings.ContainsAny(m.ide.Ed.Status, "\x00\x1b") {
			t.Fatalf("%s emitted unsafe status: %q", route, m.ide.Ed.Status)
		}
	}

	assertRejected("direct", func() { m.ide.OpenFileAt("tui.test") })
	if m.ide.Ed.HasSelection() || m.pendingSelection != nil || m.openIDESelectionMenu(2, 2) {
		t.Fatal("rejected open retained a selection that could be sent to Ask Maestro")
	}

	// Ctrl-P picker keeps tracked/untracked binaries discoverable, then routes
	// the selected value through the same guarded open path.
	m.ide.Focus = ideEditor
	action := m.ide.Ed.Update(editor.Key{Kind: editor.KeyCtrlP})
	m.ide.handleAction(m, action)
	if !containsString(m.ide.Ed.Picker.Items, "tui.test") {
		t.Fatalf("binary disappeared from picker instead of being refused: %v", m.ide.Ed.Picker.Items)
	}
	m.ide.Ed.Picker.Query = "tui.test"
	assertRejected("picker", func() { m.ide.Ed.Update(editor.Key{Kind: editor.KeyEnter}) })

	// File tree mouse/keyboard dispatch.
	m.ide.refreshFiles()
	entries := m.ide.treeEntries()
	treeIndex := -1
	for i, entry := range entries {
		if entry.Path == "tui.test" {
			treeIndex = i
			break
		}
	}
	if treeIndex < 0 {
		t.Fatalf("binary missing from file tree: %#v", entries)
	}
	assertRejected("tree", func() { m.dispatchRegion(Region{Action: ActionOpenFile, Index: treeIndex}) })

	// CHANGES rail dispatch.
	m.sidebar.modFiles = []git.NumStat{{Path: "tui.test", Untracked: true}}
	assertRejected("changes", func() { m.dispatchRegion(Region{Action: ActionOpenChanged, Index: 0}) })

	// Vim :e command reports through handleAction without exposing the error's
	// path or ever switching the active buffer.
	m.ide.Focus = ideEditor
	m.ide.Ed.SetKeymap("vim")
	assertRejected(":e", func() {
		feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{':'}})
		for _, r := range "e tui.test" {
			msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
			if r == ' ' {
				msg = tea.KeyMsg{Type: tea.KeySpace}
			}
			feed(m, msg)
		}
		feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	})

	view := m.View()
	if !utf8.ValidString(view) || strings.Contains(view, string(binary)) {
		t.Fatalf("IDE view contains binary or invalid UTF-8 after refusal")
	}
}

func TestWorkspaceLocationStopsAfterRejectedBinaryOpen(t *testing.T) {
	m, project := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 32})
	if err := os.WriteFile(filepath.Join(project, "safe.txt"), []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "tui.test"), []byte("MZprintable"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.ToggleIDE()
	if !m.ide.OpenFileAt("safe.txt") {
		t.Fatal("open safe fixture")
	}
	active := m.ide.Ed.Buffer()
	active.Cur = editor.Cursor{Line: 1, Col: 2}
	m.openWorkspaceLocation("tui.test", 1, 1, true)
	if m.ide.Ed.Buffer() != active || active.Cur != (editor.Cursor{Line: 1, Col: 2}) {
		t.Fatalf("rejected transcript path moved active editor: buffer=%p cursor=%+v", m.ide.Ed.Buffer(), active.Cur)
	}
}
