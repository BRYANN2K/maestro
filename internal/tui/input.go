package tui

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// inputBox wraps bubbles/textarea with the Maestro sauce: Enter sends
// (handled by the app), Shift+Enter newline, ↑/↓ history when the cursor is
// at the boundaries, /command detection, placeholder, dynamic height up to
// half the screen (Phase 6), and draft persistence (B11 §11.3).
type inputBox struct {
	ta                 textarea.Model
	styles             Styles
	history            []string
	histIdx            int
	lastDraft          string
	height             int
	width              int
	replaceOnFirstEdit bool
}

// newInputBox builds the input.
func newInputBox(styles Styles) *inputBox {
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetWidth(60)
	ta.SetHeight(1)
	// Maestro keymap: Shift+Enter inserts a newline; Enter is intercepted by
	// the app for sending.
	ta.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	ta.Focus()
	return &inputBox{ta: ta, styles: styles}
}

// Value returns the current input.
func (ib *inputBox) Value() string { return ib.ta.Value() }

// String is the legacy accessor.
func (ib *inputBox) String() string { return ib.ta.Value() }

// Set replaces the value and focuses.
func (ib *inputBox) Set(s string) {
	ib.ta.SetValue(s)
	ib.ta.CursorEnd()
	ib.replaceOnFirstEdit = false
	ib.ta.Focus()
}

// armReplacement makes the next edit replace the current value. It is used
// by the selection editor: the selected code remains visible as context, but
// the first typed character behaves like a normal text selection replacement.
func (ib *inputBox) armReplacement() { ib.replaceOnFirstEdit = true }

func (ib *inputBox) replaceIfArmed() {
	if !ib.replaceOnFirstEdit {
		return
	}
	ib.ta.SetValue("")
	ib.ta.CursorEnd()
	ib.replaceOnFirstEdit = false
}

// update handles a key message; returns whether the key was consumed.
func (ib *inputBox) update(msg tea.KeyMsg) bool {
	// History navigation: ↑ on the first line of a single-line input; ↓
	// on the last line. Multiline inputs keep cursor movement.
	if msg.Type == tea.KeyUp && ib.ta.Line() == 0 && !strings.Contains(ib.ta.Value(), "\n") {
		ib.historyBack()
		return true
	}
	if msg.Type == tea.KeyDown && ib.ta.Line() == ib.ta.LineCount()-1 {
		ib.historyForward()
		return true
	}
	if msg.Type == tea.KeySpace {
		ib.replaceIfArmed()
		ib.ta.InsertString(" ")
		return true
	}
	if msg.Type == tea.KeyRunes {
		clean := sanitizeInput(string(msg.Runes))
		if clean != string(msg.Runes) {
			if clean != "" {
				ib.replaceIfArmed()
				ib.ta.InsertString(clean)
			}
			return true
		}
		ib.replaceIfArmed()
	}
	if msg.Type == tea.KeyBackspace || msg.Type == tea.KeyDelete {
		ib.replaceIfArmed()
	}
	ib.ta, _ = ib.ta.Update(msg)
	return true
}

// insertNewline inserts a newline at the cursor (Shift+Enter).
func (ib *inputBox) insertNewline() {
	ib.ta.InsertString("\n")
}

// pushHistory stores a sent prompt.
func (ib *inputBox) pushHistory(prompt string) {
	if prompt == "" {
		return
	}
	ib.history = append(ib.history, prompt)
	ib.histIdx = len(ib.history)
	ib.lastDraft = ""
}

func (ib *inputBox) historyBack() {
	if len(ib.history) == 0 {
		return
	}
	if ib.histIdx == len(ib.history) {
		ib.lastDraft = ib.ta.Value()
	}
	if ib.histIdx > 0 {
		ib.histIdx--
		ib.ta.SetValue(ib.history[ib.histIdx])
		ib.ta.CursorEnd()
	}
}

func (ib *inputBox) historyForward() {
	if ib.histIdx >= len(ib.history) {
		return
	}
	ib.histIdx++
	if ib.histIdx == len(ib.history) {
		ib.ta.SetValue(ib.lastDraft)
	} else {
		ib.ta.SetValue(ib.history[ib.histIdx])
	}
	ib.ta.CursorEnd()
}

// setWidth sizes the textarea without collapsing its current height. Layout
// runs after every edit; resetting to one row here made a growing textarea
// behave like a horizontally scrolling input between Update and View.
func (ib *inputBox) setWidth(w int) bool {
	w = max(w, 10)
	if w == ib.width && ib.ta.Width() == w {
		return false
	}
	ib.ta.SetWidth(w)
	ib.width = w
	return true
}

// neededHeight computes the rendered height for the current value at the
// given width. A fresh composer is one line tall and grows only when the
// prompt actually wraps or contains newlines.
func (ib *inputBox) neededHeight(width, maxH int) int {
	maxH = max(maxH, 1)
	width = max(width, 10)
	lines := 0
	// Ask a copy of the actual textarea for its soft-wrap height. A rune-count
	// approximation under-counts word wrapping and double-width glyphs, which
	// caused the editor to start internally scrolling before the dock grew.
	probe := ib.ta
	probe.SetWidth(width)
	for _, line := range strings.Split(ib.ta.Value(), "\n") {
		probe.SetValue(line)
		probe.CursorEnd()
		lines += max(probe.LineInfo().Height, 1)
	}
	return clamp(lines, 1, maxH)
}

// cursorAtEnd reports whether resizing may safely rebuild the textarea's
// viewport without changing the user's edit position.
func (ib *inputBox) cursorAtEnd() bool {
	physical := strings.Split(ib.ta.Value(), "\n")
	if len(physical) == 0 || ib.ta.Line() != len(physical)-1 {
		return false
	}
	info := ib.ta.LineInfo()
	return info.StartColumn+info.ColumnOffset >= len([]rune(physical[len(physical)-1]))
}

// resize synchronizes the textarea widget and the surrounding dock before
// rendering. While the dock is still below its cap, keep the internal
// viewport at the top so every newly wrapped row remains visible.
func (ib *inputBox) resize(width, maxH int) int {
	widthChanged := ib.setWidth(width)
	h := ib.neededHeight(width, maxH)
	sizeChanged := h != ib.height
	if sizeChanged || ib.ta.Height() != h {
		ib.ta.SetHeight(h)
		ib.height = h
	}
	if (sizeChanged || widthChanged) && ib.cursorAtEnd() && h < maxH {
		value := ib.ta.Value()
		ib.ta.SetValue(value) // Reset also returns the internal viewport to top.
		ib.ta.CursorEnd()
	}
	return h
}

// sizedView renders the textarea at the height the current value needs,
// growing up to maxH (half the screen) as the prompt becomes multi-line.
func (ib *inputBox) sizedView(width, maxH int) string {
	width = max(width, 10)
	ib.resize(width, maxH)
	v := ib.ta.View()
	return strings.TrimSuffix(v, "\n")
}

// view renders the textarea content (without its own borders).
func (ib *inputBox) view(width int) string {
	v := ib.ta.View()
	// Strip the trailing newline textarea adds.
	return strings.TrimSuffix(v, "\n")
}

// sanitizeInput prevents terminal control sequences (notably leaked SGR mouse
// reports from terminal adapters) from becoming visible chat text.
func sanitizeInput(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\r':
			return -1
		case r == '\n' || r == '\t':
			return r
		case unicode.IsControl(r):
			return -1
		default:
			return r
		}
	}, s)
}

// sanitizePastedInput normalizes terminal line endings before applying the
// shared text sanitizer. tmux paste-buffer uses CR as its default line
// separator even inside bracketed-paste framing; dropping those CR bytes
// would silently collapse a multiline paste into one line.
func sanitizePastedInput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return sanitizeInput(s)
}

// sanitizeSingleLineInput keeps paste data from injecting layout rows into
// filters and other one-line fields. Separators become ordinary spaces so
// pasted words do not get concatenated.
func sanitizeSingleLineInput(s string) string {
	s = sanitizePastedInput(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, s)
}

// draftPath is the single-line prompt draft persisted across launches.
func draftPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".maestro", "draft"), nil
}

// SaveDraft persists a non-trivial prompt so an accidental quit never loses
// it (opencode-style draft retention).
func SaveDraft(value string) {
	if len(strings.TrimSpace(value)) < 10 {
		return
	}
	path, err := draftPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strings.TrimSpace(value)), 0o600)
}

// LoadDraft returns the saved draft and clears it. Non-trivial drafts are
// restored into the prompt at startup.
func LoadDraft() string {
	path, err := draftPath()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	_ = os.Remove(path)
	draft := strings.TrimSpace(string(data))
	if len(draft) < 10 {
		return ""
	}
	return draft
}
