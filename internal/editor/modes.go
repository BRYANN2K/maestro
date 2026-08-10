package editor

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// KeyKind is a normalized key event (TUI translates tea.KeyMsg into this).
type KeyKind int

// Key kinds.
const (
	KeyRune KeyKind = iota
	KeyEnter
	KeyEsc
	KeyBackspace
	KeyDelete
	KeyLeft
	KeyRight
	KeyUp
	KeyDown
	KeyHome
	KeyEnd
	KeyCtrlR
	KeyCtrlS
	KeyCtrlZ
	KeyCtrlY
	KeyCtrlA
	KeyCtrlP
	KeySpace
	KeyTab
)

// Key is one normalized editor key.
type Key struct {
	Kind  KeyKind
	Runes []rune
	Shift bool
	Ctrl  bool
}

// RuneKey builds a single-rune key.
func RuneKey(r rune) Key { return Key{Kind: KeyRune, Runes: []rune{r}} }

// TextKey builds a multi-rune paste key.
func TextKey(s string) Key { return Key{Kind: KeyRune, Runes: []rune(s)} }

// KeymapMode selects the editing interaction model. Standard is intentionally
// the default; Vim is an opt-in compatibility mode for modal editing.
type KeymapMode string

const (
	KeymapStandard KeymapMode = "standard"
	KeymapVim      KeymapMode = "vim"
)

// hasControlRunes reports whether the runes contain terminal control bytes.
func hasControlRunes(rs []rune) bool {
	for _, r := range rs {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return true
		}
	}
	return false
}

// sanitizePasteText keeps the text controls editors can represent safely.
// Bracketed paste is trusted as user input, but arbitrary C0/C1 controls must
// not reach the terminal renderer as file content.
func sanitizePasteText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = ansi.Strip(text)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, text)
}

// Mode is the editor mode (§5.2.1).
type Mode int

// Modes.
const (
	ModeNormal Mode = iota
	ModeInsert
	ModeVisual
	ModeVisualLine
	ModeVisualBlock
	ModeCommand
)

// String renders the mode name.
func (m Mode) String() string {
	switch m {
	case ModeNormal:
		return "NORMAL"
	case ModeInsert:
		return "INSERT"
	case ModeVisual:
		return "VISUAL"
	case ModeVisualLine:
		return "VISUAL LINE"
	case ModeVisualBlock:
		return "VISUAL BLOCK"
	case ModeCommand:
		return "COMMAND"
	}
	return "?"
}

// EditAction is what the editor asks the TUI to do after a key.
type EditAction int

// Actions.
const (
	ActNone         EditAction = iota
	ActQuitIDE                 // :q — back to chat mode
	ActQuitApp                 // :qa
	ActAgentReview             // :AgentReview overlay
	ActGitWorkspace            // Space G overlay
	ActPicker                  // Ctrl-P overlay
	ActHunkStage               // :hunk stage
	ActSave                    // :w
	ActOpenFile                // :e <path>
	ActAskAgent                // Space a with a visual selection
)

// Visual selection state.
type visualState struct {
	Start  Cursor
	Line   bool
	Block  bool
	Active bool
}

// Editor is the pure-Go modal editor core.
type Editor struct {
	Buffers []*Buffer
	CurBuf  int
	Mode    Mode
	Keymap  KeymapMode
	Project string // project root (for :e and the file tree)

	cmdLine    string
	cmdStart   bool
	pending    []rune // normal-mode key sequence
	count      int
	yanked     []string
	yankedLine bool

	lastSearch string
	searchPos  Cursor

	visual visualState

	standardAnchor    Cursor
	standardSelecting bool
	lastOpenError     error

	Status string // transient status flash

	// External hooks the TUI fills in.
	OpenFile    func(path string) error
	SaveBuffer  func(b *Buffer) error
	StageHunks  func(b *Buffer) error
	ProposalSrc func() []ReviewProposal

	// Sub-states.
	Review   *ReviewState
	Git      *GitWorkspace
	Picker   *Picker
	Sessions *SessionStore
	Crash    *CrashStore
}

// NewEditor builds an editor with one buffer.
func NewEditor(project string) *Editor {
	e := &Editor{
		Project: project,
		Mode:    ModeNormal,
		Keymap:  KeymapStandard,
		Review:  NewReviewState(),
		Picker:  NewPicker(),
	}
	e.Sessions = NewSessionStore("")
	e.Crash = NewCrashStore("")
	return e
}

// SetKeymap changes the editor interaction model and clears transient state.
func (e *Editor) SetKeymap(mode string) {
	if mode == string(KeymapVim) {
		e.Keymap = KeymapVim
	} else {
		e.Keymap = KeymapStandard
	}
	e.Mode = ModeNormal
	e.pending = nil
	e.count = 0
	e.clearSelection()
}

// IsVim reports whether modal Vim interaction is enabled.
func (e *Editor) IsVim() bool { return e.Keymap == KeymapVim }

// DisplayMode is the compact status label shown by the editor renderer.
func (e *Editor) DisplayMode() string {
	if !e.IsVim() {
		return "EDIT"
	}
	return e.Mode.String()
}

// Buffer returns the active buffer.
func (e *Editor) Buffer() *Buffer {
	if len(e.Buffers) == 0 {
		return nil
	}
	return e.Buffers[e.CurBuf]
}

// Open loads a file into a new buffer (or focuses an existing one).
func (e *Editor) Open(path string) error {
	for i, b := range e.Buffers {
		if b.Path == path {
			e.CurBuf = i
			e.lastOpenError = nil
			return nil
		}
	}
	b, err := Load(path)
	if err != nil {
		e.lastOpenError = err
		return err
	}
	e.Buffers = append(e.Buffers, b)
	e.CurBuf = len(e.Buffers) - 1
	e.Mode = ModeNormal
	e.lastOpenError = nil
	return nil
}

// LastOpenError reports the most recent Open failure. It lets the TUI route a
// command-mode :e error without inferring state from human-facing strings.
func (e *Editor) LastOpenError() error { return e.lastOpenError }

// HasSelection reports whether a visual selection has content.
func (e *Editor) HasSelection() bool {
	_, _, _, _, ok := e.selectionBounds()
	return ok
}

func (e *Editor) selectionBounds() (start, end Cursor, line, block bool, ok bool) {
	b := e.Buffer()
	if b == nil {
		return Cursor{}, Cursor{}, false, false, false
	}
	if e.standardSelecting {
		start, end = e.standardAnchor, b.Cur
	} else if e.visual.Active && e.Mode >= ModeVisual && e.Mode <= ModeVisualBlock {
		start, end = e.visual.Start, b.Cur
		line, block = e.visual.Line, e.visual.Block
	} else {
		return Cursor{}, Cursor{}, false, false, false
	}
	if start.Line > end.Line || (start.Line == end.Line && start.Col > end.Col) {
		start, end = end, start
	}
	if start == end && !line {
		return Cursor{}, Cursor{}, false, false, false
	}
	return start, end, line, block, true
}

// SelectionText returns the active visual selection and its range.
func (e *Editor) SelectionText() (string, Cursor, Cursor, bool) {
	start, end, line, block, ok := e.selectionBounds()
	if !ok || e.Buffer() == nil {
		return "", Cursor{}, Cursor{}, false
	}
	b := e.Buffer()
	if line {
		return strings.Join(b.Lines[start.Line:end.Line+1], "\n"), start, end, true
	}
	if block {
		lo, hi := start.Col, end.Col
		if lo > hi {
			lo, hi = hi, lo
		}
		var parts []string
		for line := start.Line; line <= end.Line; line++ {
			runes := []rune(b.LineText(line))
			loClamped := min(lo, len(runes))
			hiClamped := min(hi+1, len(runes))
			if hiClamped < loClamped {
				hiClamped = loClamped
			}
			parts = append(parts, string(runes[loClamped:hiClamped]))
		}
		return strings.Join(parts, "\n"), start, end, true
	}
	return selectionText(b, start, end), start, end, true
}

// SelectionContains reports whether a buffer cell belongs to the active
// selection. It is used by both standard and Vim renderers.
func (e *Editor) SelectionContains(line, col int) bool {
	start, end, lineMode, blockMode, ok := e.selectionBounds()
	if !ok {
		return false
	}
	if lineMode {
		return line >= start.Line && line <= end.Line
	}
	if blockMode {
		lo, hi := start.Col, end.Col
		if lo > hi {
			lo, hi = hi, lo
		}
		return line >= start.Line && line <= end.Line && col >= lo && col <= hi
	}
	if line < start.Line || line > end.Line {
		return false
	}
	if start.Line == end.Line {
		return col >= start.Col && col < end.Col
	}
	if line == start.Line {
		return col >= start.Col
	}
	if line == end.Line {
		return col < end.Col
	}
	return true
}

// SelectionRange returns the half-open selected column interval for one
// source line. Renderers use it once per line instead of recomputing the
// complete selection bounds for every visible character.
func (e *Editor) SelectionRange(line, lineLen int) (start, end int, ok bool) {
	from, to, lineMode, blockMode, active := e.selectionBounds()
	if !active || line < from.Line || line > to.Line {
		return 0, 0, false
	}
	if lineMode {
		return 0, lineLen, lineLen > 0
	}
	if blockMode {
		lo, hi := from.Col, to.Col
		if lo > hi {
			lo, hi = hi, lo
		}
		lo = min(lo, lineLen)
		hi = min(hi+1, lineLen)
		return lo, hi, hi > lo
	}
	start, end = 0, lineLen
	if line == from.Line {
		start = min(from.Col, lineLen)
	}
	if line == to.Line {
		end = min(to.Col, lineLen)
	}
	return start, end, end > start
}

// BeginVisualAt starts a mouse-driven character selection.
func (e *Editor) BeginVisualAt(c Cursor) {
	if b := e.Buffer(); b != nil {
		b.Cur = c
		b.clamp()
		e.startVisual(b, false, false)
	}
}

// BeginSelectionAt starts a selection using the active keymap semantics.
func (e *Editor) BeginSelectionAt(c Cursor) {
	if e.IsVim() {
		e.BeginVisualAt(c)
		return
	}
	if b := e.Buffer(); b != nil {
		b.Cur = c
		b.clamp()
		e.visual.Active = false
		e.standardAnchor = b.Cur
		e.standardSelecting = true
	}
}

// UpdateVisualCursor moves the active visual selection cursor.
func (e *Editor) UpdateVisualCursor(c Cursor) {
	if b := e.Buffer(); b != nil && e.visual.Active && e.Mode >= ModeVisual && e.Mode <= ModeVisualBlock {
		b.Cur = c
		b.clamp()
	}
}

// UpdateSelectionCursor moves the active selection cursor.
func (e *Editor) UpdateSelectionCursor(c Cursor) {
	if e.IsVim() {
		e.UpdateVisualCursor(c)
		return
	}
	if b := e.Buffer(); b != nil && e.standardSelecting {
		b.Cur = c
		b.clamp()
	}
}

// CancelVisual exits visual mode without editing the buffer.
func (e *Editor) CancelVisual() {
	e.clearSelection()
}

// CancelSelection clears either a standard or Vim selection.
func (e *Editor) CancelSelection() { e.clearSelection() }

func (e *Editor) clearSelection() {
	e.visual.Active = false
	e.standardSelecting = false
	if e.Mode >= ModeVisual && e.Mode <= ModeVisualBlock {
		e.Mode = ModeNormal
	}
}

// ReplaceSelection replaces the active selection and records one undo step.
func (e *Editor) ReplaceSelection(text string) bool {
	b := e.Buffer()
	start, end, lineMode, blockMode, ok := e.selectionBounds()
	if !ok || b == nil {
		return false
	}
	b.pushUndo()
	if lineMode {
		replacement := strings.Split(text, "\n")
		lines := append([]string(nil), b.Lines[:start.Line]...)
		lines = append(lines, replacement...)
		lines = append(lines, b.Lines[end.Line+1:]...)
		b.Lines = lines
		b.Cur = Cursor{Line: start.Line, Col: 0}
	} else if blockMode {
		lo, hi := start.Col, end.Col
		if lo > hi {
			lo, hi = hi, lo
		}
		replacement := strings.Split(text, "\n")
		for line := start.Line; line <= end.Line; line++ {
			runes := []rune(b.LineText(line))
			left := string(runes[:min(lo, len(runes))])
			rightStart := min(hi+1, len(runes))
			right := string(runes[rightStart:])
			part := replacement[min(line-start.Line, len(replacement)-1)]
			b.Lines[line] = left + part + right
		}
		b.Cur = Cursor{Line: start.Line, Col: lo + len([]rune(replacement[0]))}
	} else {
		replacement := strings.Split(text, "\n")
		leftRunes := []rune(b.LineText(start.Line))
		rightRunes := []rune(b.LineText(end.Line))
		left := string(leftRunes[:min(start.Col, len(leftRunes))])
		rightStart := min(end.Col, len(rightRunes))
		right := string(rightRunes[rightStart:])
		lines := append([]string(nil), b.Lines[:start.Line]...)
		if len(replacement) == 1 {
			lines = append(lines, left+replacement[0]+right)
		} else {
			lines = append(lines, left+replacement[0])
			lines = append(lines, replacement[1:len(replacement)-1]...)
			lines = append(lines, replacement[len(replacement)-1]+right)
		}
		lines = append(lines, b.Lines[end.Line+1:]...)
		b.Lines = lines
		b.Cur = Cursor{Line: start.Line + len(replacement) - 1, Col: len([]rune(replacement[len(replacement)-1]))}
	}
	if len(b.Lines) == 0 {
		b.Lines = []string{""}
	}
	b.markDirty()
	e.clearSelection()
	return true
}

// Close closes the active buffer.
func (e *Editor) Close() {
	if len(e.Buffers) == 0 {
		return
	}
	e.Buffers = append(e.Buffers[:e.CurBuf], e.Buffers[e.CurBuf+1:]...)
	if e.CurBuf >= len(e.Buffers) {
		e.CurBuf = len(e.Buffers) - 1
	}
}

// Update processes one key. Returns the action the TUI should perform.
func (e *Editor) Update(k Key) EditAction {
	// Sub-mode routing: picker / git workspace / agent review / command line.
	if e.Picker.Active {
		return e.updatePicker(k)
	}
	if e.Git != nil && e.Git.Active {
		return e.updateGit(k)
	}
	if e.Review.Active {
		return e.updateReview(k)
	}
	if e.Mode == ModeCommand {
		return e.updateCommand(k)
	}
	b := e.Buffer()
	if b == nil {
		return ActNone
	}
	if !e.IsVim() {
		return e.updateStandard(k, b)
	}
	switch e.Mode {
	case ModeInsert:
		return e.updateInsert(k, b)
	case ModeVisual, ModeVisualLine, ModeVisualBlock:
		return e.updateVisual(k, b)
	default:
		return e.updateNormal(k, b)
	}
}

// Paste inserts bracketed-paste text literally without passing it through
// modal keymaps. Buffer edits are recorded as one undo transaction.
func (e *Editor) Paste(text string) EditAction {
	text = sanitizePasteText(text)
	if text == "" {
		return ActNone
	}

	// Text-entry overlays accept pasted text, but pasted shortcut letters
	// never trigger their actions.
	if e.Picker.Active {
		e.Picker.Query += strings.NewReplacer("\n", " ", "\t", " ").Replace(text)
		e.Picker.Sel = 0
		return ActNone
	}
	if e.Git != nil && e.Git.Active {
		e.Git.Message += text
		return ActNone
	}
	if e.Review.Active {
		return ActNone
	}
	if e.Mode == ModeCommand {
		e.cmdLine += text
		return ActNone
	}

	b := e.Buffer()
	if b == nil {
		return ActNone
	}
	e.pending = nil
	e.count = 0
	if !e.ReplaceSelection(text) {
		b.InsertText(text)
	}
	return ActNone
}

// updateStandard implements the non-modal editor used by default.
func (e *Editor) updateStandard(k Key, b *Buffer) EditAction {
	switch k.Kind {
	case KeyEsc:
		e.clearSelection()
	case KeyCtrlS:
		if e.SaveBuffer != nil {
			if err := e.SaveBuffer(b); err != nil {
				e.Status = "error: " + err.Error()
				return ActNone
			}
		}
		e.Status = "saved " + b.Path
		return ActSave
	case KeyCtrlZ:
		b.Undo()
	case KeyCtrlY:
		b.Redo()
	case KeyCtrlA:
		e.standardAnchor = Cursor{}
		b.Cur = Cursor{Line: len(b.Lines) - 1, Col: len([]rune(b.LineText(len(b.Lines) - 1)))}
		b.clamp()
		e.standardSelecting = true
	case KeyLeft, KeyRight, KeyUp, KeyDown, KeyHome, KeyEnd:
		e.moveStandard(k, b)
	case KeyBackspace:
		if !e.ReplaceSelection("") {
			b.Backspace()
		}
	case KeyDelete:
		if !e.ReplaceSelection("") {
			b.DeleteChar()
		}
	case KeyEnter:
		if !e.ReplaceSelection("\n") {
			b.InsertNewline()
		}
	case KeySpace:
		e.insertStandard(" ", b)
	case KeyTab:
		e.insertStandard("  ", b)
	case KeyCtrlP:
		return ActPicker
	case KeyRune:
		if hasControlRunes(k.Runes) {
			return ActNone
		}
		e.insertStandard(string(k.Runes), b)
	}
	return ActNone
}

func (e *Editor) insertStandard(text string, b *Buffer) {
	if e.ReplaceSelection(text) {
		return
	}
	b.InsertText(text)
}

func (e *Editor) moveStandard(k Key, b *Buffer) {
	if k.Shift {
		if !e.standardSelecting {
			e.standardAnchor = b.Cur
			e.standardSelecting = true
		}
	} else {
		e.standardSelecting = false
	}
	switch k.Kind {
	case KeyLeft:
		b.Move(MotLeft, 1)
	case KeyRight:
		b.Move(MotRight, 1)
	case KeyUp:
		b.Move(MotUp, 1)
	case KeyDown:
		b.Move(MotDown, 1)
	case KeyHome:
		b.Move(MotLineStart, 1)
	case KeyEnd:
		b.Move(MotLineEnd, 1)
	}
}

// ---- insert mode ---------------------------------------------------------

func (e *Editor) updateInsert(k Key, b *Buffer) EditAction {
	switch k.Kind {
	case KeyEsc:
		e.Mode = ModeNormal
		b.Cur.Col--
		b.clamp()
	case KeyEnter:
		b.InsertNewline()
	case KeyBackspace:
		b.Backspace()
	case KeyDelete:
		b.DeleteChar()
	case KeyLeft:
		b.Move(MotLeft, 1)
	case KeyRight:
		b.Move(MotRight, 1)
	case KeyUp:
		b.Move(MotUp, 1)
	case KeyDown:
		b.Move(MotDown, 1)
	case KeyRune:
		// Terminal control noise (unparsed mouse/CSI bytes) must never
		// become file content. A KeyRunes message containing any control
		// byte is unparsed terminal input, not a real paste — drop it
		// entirely (bubbletea delivers genuine pastes as PasteMsg).
		if hasControlRunes(k.Runes) {
			return ActNone
		}
		b.InsertText(string(k.Runes))
	case KeySpace:
		b.InsertRune(' ')
	case KeyTab:
		b.InsertText("  ")
	}
	return ActNone
}

// ---- normal mode ---------------------------------------------------------

func (e *Editor) updateNormal(k Key, b *Buffer) EditAction {
	switch k.Kind {
	case KeyEsc:
		e.pending = nil
		e.count = 0
		return ActNone
	case KeyRune:
		return e.normalRune(k.Runes, b)
	case KeyEnter:
		// Enter in normal mode: move to first non-blank on next line.
		b.Cur.Line++
		b.Cur.Col = 0
		b.clamp()
	case KeyLeft:
		b.Move(MotLeft, e.countOr(1))
	case KeyRight:
		b.Move(MotRight, e.countOr(1))
	case KeyUp:
		b.Move(MotUp, e.countOr(1))
	case KeyDown:
		b.Move(MotDown, e.countOr(1))
	case KeyCtrlR:
		b.Redo()
	case KeyCtrlS:
		if e.SaveBuffer != nil {
			if err := e.SaveBuffer(b); err != nil {
				e.Status = "error: " + err.Error()
				return ActNone
			}
		}
		e.Status = "saved " + b.Path
		return ActSave
	case KeyCtrlP:
		return ActPicker
	}
	return ActNone
}

func (e *Editor) normalRune(rs []rune, b *Buffer) EditAction {
	if len(rs) == 0 {
		return ActNone
	}
	// Counts: digits accumulate before a motion/operator.
	if len(rs) == 1 && rs[0] >= '1' && rs[0] <= '9' {
		e.count = e.count*10 + int(rs[0]-'0')
		return ActNone
	}
	count := e.countOr(1)
	e.count = 0

	seq := string(append(append([]rune(nil), e.pending...), rs...))
	e.pending = nil
	switch seq {
	case "h":
		b.Move(MotLeft, count)
	case "l":
		b.Move(MotRight, count)
	case "j":
		b.Move(MotDown, count)
	case "k":
		b.Move(MotUp, count)
	case "w":
		b.Move(MotWordForward, count)
	case "b":
		b.Move(MotWordBack, count)
	case "e":
		b.Move(MotWordEnd, count)
	case "0":
		b.Move(MotLineStart, 1)
	case "$":
		b.Move(MotLineEnd, 1)
	case "gg":
		b.Move(MotDocStart, 1)
	case "G":
		b.Move(MotDocEnd, 1)
	case "%":
		b.Move(MotMatchPair, 1)
	case "x":
		for i := 0; i < count; i++ {
			b.DeleteChar()
		}
	case "dd":
		for i := 0; i < count; i++ {
			e.yanked = []string{b.DeleteLine()}
			e.yankedLine = true
		}
	case "yy":
		e.yanked = []string{b.LineText(b.Cur.Line)}
		e.yankedLine = true
	case "p":
		e.paste(b, false)
	case "P":
		e.paste(b, true)
	case "u":
		b.Undo()
	case "i":
		e.Mode = ModeInsert
	case "a":
		e.Mode = ModeInsert
		b.Cur.Col++
		b.clamp()
	case "I":
		e.Mode = ModeInsert
		b.Cur.Col = 0
	case "A":
		e.Mode = ModeInsert
		b.Cur.Col = len([]rune(b.LineText(b.Cur.Line)))
	case "o":
		b.InsertLineAfter()
		e.Mode = ModeInsert
	case "O":
		b.InsertLineBefore()
		e.Mode = ModeInsert
	case "v":
		e.startVisual(b, false, false)
	case "V":
		e.startVisual(b, true, false)
	case "d":
		// dw / dd via pending accumulation
		e.pending = []rune{'d'}
	case "dw":
		b.DeleteWord()
	case "y":
		e.pending = []rune{'y'}
	case "f", "F":
		e.pending = []rune(seq)
	case "t", "T":
		e.pending = []rune(seq)
	case "n":
		e.searchNext(b, true)
	case "N":
		e.searchNext(b, false)
	case "m":
		e.pending = []rune{'m'}
	case "ma", "mb", "mc", "md", "me", "mf", "mg", "mh", "mi", "mj", "mk", "ml", "mm", "mn", "mo", "mp", "mq", "mr", "ms", "mt", "mu", "mv", "mw", "mx", "my", "mz":
		b.SetMark(seq[1])
	case "'a", "'b", "'c", "'d", "'e", "'f", "'g", "'h", "'i", "'j", "'k", "'l", "'m", "'n", "'o", "'p", "'q", "'r", "'s", "'t", "'u", "'v", "'w", "'x", "'y", "'z":
		b.JumpMark(seq[1])
	case ":":
		e.Mode = ModeCommand
		e.cmdLine = ""
		e.cmdStart = true
		return ActNone
	default:
		// two-char sequences that are waiting for a char argument
		if len(seq) == 2 && (seq[0] == 'f' || seq[0] == 't' || seq[0] == 'F' || seq[0] == 'T') {
			ch := []rune(seq)[1]
			if seq[0] == 'f' || seq[0] == 'F' {
				b.FindChar(ch)
			} else {
				b.ToChar(ch)
			}
			return ActNone
		}
		if len(seq) == 1 && (seq == "f" || seq == "t" || seq == "F" || seq == "T" || seq == "d" || seq == "y" || seq == "m") {
			// waiting for a second key
			e.pending = []rune(seq)
			return ActNone
		}
		if len(seq) == 2 && seq[0] == '/' {
			e.lastSearch = string(seq[1])
			e.searchNext(b, true)
			return ActNone
		}
	}
	return ActNone
}

func (e *Editor) countOr(def int) int {
	if e.count > 0 {
		return e.count
	}
	return def
}

func (e *Editor) paste(b *Buffer, before bool) {
	if len(e.yanked) == 0 {
		return
	}
	b.pushUndo()
	lines := append([]string(nil), e.yanked...)
	at := b.Cur.Line
	if !before {
		at++
	}
	insert := append([]string{}, b.Lines[:at]...)
	insert = append(insert, lines...)
	insert = append(insert, b.Lines[at:]...)
	b.Lines = insert
	if !before && at <= b.Cur.Line {
		b.Cur.Line += len(lines) - 1
	}
	b.Cur.Col = 0
	b.markDirty()
}

// ---- visual mode ---------------------------------------------------------

func (e *Editor) startVisual(b *Buffer, line, block bool) {
	e.visual = visualState{Start: b.Cur, Line: line, Block: block, Active: true}
	e.Mode = ModeVisual
	if line {
		e.Mode = ModeVisualLine
	}
	if block {
		e.Mode = ModeVisualBlock
	}
}

func (e *Editor) updateVisual(k Key, b *Buffer) EditAction {
	switch k.Kind {
	case KeyEsc:
		e.visual.Active = false
		e.Mode = ModeNormal
	case KeyLeft:
		b.Move(MotLeft, 1)
	case KeyRight:
		b.Move(MotRight, 1)
	case KeyUp:
		b.Move(MotUp, 1)
	case KeyDown:
		b.Move(MotDown, 1)
	case KeyRune:
		switch string(k.Runes) {
		case "w":
			b.Move(MotWordForward, 1)
		case "b":
			b.Move(MotWordBack, 1)
		case "0":
			b.Move(MotLineStart, 1)
		case "$":
			b.Move(MotLineEnd, 1)
		case "y":
			e.yankVisual(b)
			e.visual.Active = false
			e.Mode = ModeNormal
		case "d":
			e.deleteVisual(b)
			e.visual.Active = false
			e.Mode = ModeNormal
		case "v":
			e.visual.Active = false
			e.Mode = ModeNormal
		}
	}
	return ActNone
}

// selRange returns the (lo, hi) lines of the visual selection.
func (e *Editor) selRange(b *Buffer) (int, int) {
	lo, hi := e.visual.Start.Line, b.Cur.Line
	if lo > hi {
		lo, hi = hi, lo
	}
	return lo, hi
}

func (e *Editor) yankVisual(b *Buffer) {
	lo, hi := e.selRange(b)
	var lines []string
	if e.visual.Line {
		lines = append([]string(nil), b.Lines[lo:hi+1]...)
	} else {
		lines = []string{selectionText(b, e.visual.Start, b.Cur)}
	}
	e.yanked = lines
	e.yankedLine = e.visual.Line
}

func (e *Editor) deleteVisual(b *Buffer) {
	lo, hi := e.selRange(b)
	e.yankVisual(b)
	b.pushUndo()
	b.Lines = append(b.Lines[:lo], b.Lines[hi+1:]...)
	if len(b.Lines) == 0 {
		b.Lines = []string{""}
	}
	b.Cur = Cursor{Line: lo, Col: 0}
	b.clamp()
	b.markDirty()
}

// selectionText extracts the character selection between two cursors.
func selectionText(b *Buffer, a, c Cursor) string {
	lo, hi := a, c
	if lo.Line > hi.Line || (lo.Line == hi.Line && lo.Col > hi.Col) {
		lo, hi = hi, lo
	}
	if lo.Line == hi.Line {
		runes := []rune(b.LineText(lo.Line))
		return string(runes[lo.Col:hi.Col])
	}
	var parts []string
	parts = append(parts, string([]rune(b.LineText(lo.Line))[lo.Col:]))
	for l := lo.Line + 1; l < hi.Line; l++ {
		parts = append(parts, b.LineText(l))
	}
	parts = append(parts, string([]rune(b.LineText(hi.Line))[:hi.Col]))
	return strings.Join(parts, "\n")
}

// ---- command mode --------------------------------------------------------

func (e *Editor) updateCommand(k Key) EditAction {
	switch k.Kind {
	case KeyEsc:
		e.Mode = ModeNormal
		e.cmdLine = ""
	case KeyEnter:
		return e.runCommand(e.cmdLine)
	case KeyBackspace:
		if len(e.cmdLine) > 0 {
			e.cmdLine = e.cmdLine[:len(e.cmdLine)-1]
		}
	case KeyRune:
		e.cmdLine += string(k.Runes)
	case KeySpace:
		e.cmdLine += " "
	}
	return ActNone
}

func (e *Editor) runCommand(line string) EditAction {
	line = strings.TrimSpace(line)
	e.Mode = ModeNormal
	e.cmdLine = ""
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ActNone
	}
	switch fields[0] {
	case "w":
		if e.SaveBuffer != nil {
			if err := e.SaveBuffer(e.Buffer()); err != nil {
				e.Status = "error: " + err.Error()
				return ActNone
			}
		} else if err := e.Buffer().WriteFile(); err != nil {
			e.Status = "error: " + err.Error()
			return ActNone
		}
		e.Status = "saved " + e.Buffer().Path
		return ActSave
	case "q":
		return ActQuitIDE
	case "qa", "qall":
		return ActQuitApp
	case "e":
		if len(fields) < 2 {
			e.Status = ":e <path>"
			return ActNone
		}
		path := fields[1]
		if !strings.HasPrefix(path, "/") {
			path = e.Project + "/" + path
		}
		if err := e.Open(path); err != nil {
			e.Status = SafeOpenError(err)
		} else {
			e.Status = "opened " + path
		}
		return ActOpenFile
	case "AgentReview":
		e.Review.Refresh(e.ProposalSrc)
		e.Review.Active = true
		return ActAgentReview
	case "hunk":
		if len(fields) == 2 && fields[1] == "stage" {
			if e.StageHunks != nil {
				if err := e.StageHunks(e.Buffer()); err != nil {
					e.Status = "error: " + err.Error()
				} else {
					e.Status = "hunks staged"
				}
			}
			return ActHunkStage
		}
		e.Status = ":hunk stage"
	case "help", "h":
		e.Status = "w save · q leave · qa quit · e <file> · AgentReview · hunk stage"
	}
	return ActNone
}

// ---- search --------------------------------------------------------------

func (e *Editor) searchNext(b *Buffer, forward bool) {
	if e.lastSearch == "" {
		return
	}
	// search from just after the cursor
	b.Cur.Col++
	pos, ok := b.FindOnLine(e.lastSearch)
	if ok {
		b.Cur = pos
		e.searchPos = pos
		b.SetMark('/')
	}
	e.Status = fmt.Sprintf("/%s", e.lastSearch)
}
