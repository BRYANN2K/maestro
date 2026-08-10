package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSpaceLeaderOpensWhichKeyThenKeymap(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	feed(m, tea.KeyMsg{Type: tea.KeySpace})
	if m.overlay != overlayWhichKey {
		t.Fatalf("space should open which-key, got %v", m.overlay)
	}
	view := m.View()
	clean := stripANSI(view)
	if !strings.Contains(clean, "Space leader") || !strings.Contains(clean, "cycle theme") {
		t.Errorf("which-key view missing: %q", clean)
	}
	// ? after space opens the keymap (existing behavior preserved).
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.overlay != overlayKeymap {
		t.Fatalf("space ? should open keymap, got %v", m.overlay)
	}
}

func TestSpaceTeeCyclesTheme(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	before := m.orch.SettingsSnapshot().Theme
	feed(m, tea.KeyMsg{Type: tea.KeySpace})
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if after := m.orch.SettingsSnapshot().Theme; after == before {
		t.Errorf("space t should change the theme (was %q)", before)
	}
}

func TestAtFileMentionPicker(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The file list is injected for hermetic tests.
	restore := editorListFiles
	editorListFiles = func(string) []string { return []string{"main.go", "target.txt"} }
	defer func() { editorListFiles = restore }()

	m.input.Set("edit @main")
	m.input.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'.'}})
	m.maybeOpenAtFile()
	if m.overlay != overlayAtFile {
		t.Fatalf("typing @ should open the file picker, got %v", m.overlay)
	}
	list, _ := m.overlayM.(*listOverlay)
	if list.query != "main." {
		t.Errorf("picker query = %q, want main.", list.query)
	}
	if len(list.Filter()) != 1 {
		t.Errorf("filtered items = %v", list.Filter())
	}
	// Enter inserts the selection into the prompt.
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.input.Value(); got != "edit main.go" {
		t.Errorf("prompt after pick = %q", got)
	}
	if m.overlay != overlayNone {
		t.Error("overlay should close after picking")
	}
}

func TestPromptGrowsDynamically(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	if m.inputH != 1 {
		t.Fatalf("initial prompt height = %d, want 1", m.inputH)
	}
	// Three lines of text: height must grow but stay within the cap.
	m.input.Set("line one\nline two\nline three")
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	if m.inputH < 3 {
		t.Errorf("multi-line prompt height = %d", m.inputH)
	}
	view := m.View()
	if got := lipglossHeight(view); got > 38 {
		t.Errorf("view overflowed: height=%d", got)
	}
}

func TestPromptWrapsAtCappedComposerWidthWhileTyping(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 190, Height: 42})
	text := strings.Repeat("alpha beta gamma ", 20) + "TAIL"
	previousHeight := m.inputH
	for _, r := range text {
		feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if m.inputH < previousHeight {
			t.Fatalf("composer shrank while typing: %d -> %d", previousHeight, m.inputH)
		}
		previousHeight = m.inputH
	}
	if m.inputH < 3 {
		t.Fatalf("prompt height=%d: textarea did not wrap at dock width %d", m.inputH, m.inputWidth())
	}
	if got := m.input.ta.Height(); got != m.inputH {
		t.Fatalf("textarea height=%d, dock expects %d", got, m.inputH)
	}
	box := stripANSI(m.renderInputBox(m.harnessChatWidth()))
	if !strings.Contains(box, "alpha beta") || !strings.Contains(box, "TAIL") {
		t.Fatalf("wrapped first or last row was clipped from composer:\n%s", box)
	}
	if got, want := lipglossHeight(box), m.composerHeight(); got != want {
		t.Fatalf("composer rendered height=%d, want %d:\n%s", got, want, box)
	}
}

func lipglossHeight(s string) int {
	return strings.Count(s, "\n") + 1
}

func TestDraftPersistence(t *testing.T) {
	// Redirect the draft under a temp HOME so the real one is untouched.
	t.Setenv("HOME", t.TempDir())

	SaveDraft("short")
	if LoadDraft() != "" {
		t.Error("short drafts must not be persisted")
	}
	SaveDraft("a prompt that is long enough to keep")
	if got := LoadDraft(); got != "a prompt that is long enough to keep" {
		t.Errorf("draft round-trip = %q", got)
	}
	if LoadDraft() != "" {
		t.Error("draft file must be cleared after loading")
	}
}
