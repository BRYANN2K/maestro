// Package editor is Maestro's built-in modal editor (red-inspired, §5.2):
// a pure-Go core (buffer, motions, modes, highlights, git gutter, agent
// proposals, sessions) rendered by a thin bubbletea wrapper in the TUI.
package editor

import (
	"fmt"
	"os"
	"strings"
)

// Cursor is a buffer position (0-based).
type Cursor struct {
	Line int
	Col  int
}

// Snapshot is one undo state: lines + cursor.
type Snapshot struct {
	Lines []string
	Cur   Cursor
}

// Buffer is a line-based text buffer with undo/redo, marks, and dirty
// tracking. Line-based storage (a gap buffer is an optimization, not a
// semantic requirement): every edit is a snapshot-push, so undo/redo and
// crash recovery are trivial.
type Buffer struct {
	Path      string
	Lines     []string
	Cur       Cursor
	Dirty     bool
	Marks     map[byte]Cursor
	UndoStack []Snapshot
	RedoStack []Snapshot
}

// NewBuffer loads content into a fresh buffer.
func NewBuffer(path string, content []byte) *Buffer {
	lines := splitLines(string(content))
	if len(lines) == 0 {
		lines = []string{""}
	}
	return &Buffer{
		Path:  path,
		Lines: lines,
		Marks: map[byte]Cursor{},
	}
}

// Load reads a file from disk.
func Load(path string) (*Buffer, error) {
	data, err := readTextFile(path)
	if err != nil {
		return nil, err
	}
	return NewBuffer(path, data), nil
}

// String renders the buffer content.
func (b *Buffer) String() string {
	return strings.Join(b.Lines, "\n") + "\n"
}

// LineText returns the text of line i.
func (b *Buffer) LineText(i int) string {
	if i < 0 || i >= len(b.Lines) {
		return ""
	}
	return b.Lines[i]
}

// NumLines returns the line count.
func (b *Buffer) NumLines() int { return len(b.Lines) }

// clamp keeps the cursor inside the buffer.
func (b *Buffer) clamp() {
	if len(b.Lines) == 0 {
		b.Lines = []string{""}
	}
	if b.Cur.Line < 0 {
		b.Cur.Line = 0
	}
	if b.Cur.Line >= len(b.Lines) {
		b.Cur.Line = len(b.Lines) - 1
	}
	if b.Cur.Col < 0 {
		b.Cur.Col = 0
	}
	if max := len([]rune(b.LineText(b.Cur.Line))); b.Cur.Col > max {
		b.Cur.Col = max
	}
}

// pushUndo snapshots the pre-edit state.
func (b *Buffer) pushUndo() {
	snap := Snapshot{Lines: append([]string(nil), b.Lines...), Cur: b.Cur}
	b.UndoStack = append(b.UndoStack, snap)
	b.RedoStack = nil
}

// markDirty marks the buffer and clamps the cursor.
func (b *Buffer) markDirty() {
	b.Dirty = true
	b.clamp()
}

// InsertRune inserts r at the cursor.
func (b *Buffer) InsertRune(r rune) {
	b.clamp()
	b.pushUndo()
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	runes = append(runes, 0)
	copy(runes[b.Cur.Col+1:], runes[b.Cur.Col:])
	runes[b.Cur.Col] = r
	b.Lines[b.Cur.Line] = string(runes)
	b.Cur.Col++
	b.markDirty()
}

// InsertText inserts a string (multi-line aware) as one undo transaction.
func (b *Buffer) InsertText(s string) {
	if s == "" {
		return
	}
	b.clamp()
	b.pushUndo()

	line := []rune(b.LineText(b.Cur.Line))
	left := string(line[:b.Cur.Col])
	right := string(line[b.Cur.Col:])
	parts := strings.Split(s, "\n")
	if len(parts) == 1 {
		b.Lines[b.Cur.Line] = left + parts[0] + right
		b.Cur.Col += len([]rune(parts[0]))
		b.markDirty()
		return
	}

	inserted := make([]string, 0, len(parts))
	inserted = append(inserted, left+parts[0])
	inserted = append(inserted, parts[1:len(parts)-1]...)
	inserted = append(inserted, parts[len(parts)-1]+right)
	lines := make([]string, 0, len(b.Lines)+len(inserted)-1)
	lines = append(lines, b.Lines[:b.Cur.Line]...)
	lines = append(lines, inserted...)
	lines = append(lines, b.Lines[b.Cur.Line+1:]...)
	b.Lines = lines
	b.Cur.Line += len(parts) - 1
	b.Cur.Col = len([]rune(parts[len(parts)-1]))
	b.markDirty()
}

// InsertNewline splits the current line at the cursor.
func (b *Buffer) InsertNewline() {
	b.clamp()
	b.pushUndo()
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	head := string(runes[:b.Cur.Col])
	tail := string(runes[b.Cur.Col:])
	b.Lines[b.Cur.Line] = head
	b.Lines = append(b.Lines, "")
	copy(b.Lines[b.Cur.Line+2:], b.Lines[b.Cur.Line+1:])
	b.Lines[b.Cur.Line+1] = tail
	b.Cur.Line++
	b.Cur.Col = 0
	b.markDirty()
}

// Backspace removes the rune before the cursor.
func (b *Buffer) Backspace() {
	b.clamp()
	if b.Cur.Col > 0 {
		b.pushUndo()
		line := b.LineText(b.Cur.Line)
		runes := []rune(line)
		runes = append(runes[:b.Cur.Col-1], runes[b.Cur.Col:]...)
		b.Lines[b.Cur.Line] = string(runes)
		b.Cur.Col--
		b.markDirty()
		return
	}
	if b.Cur.Line > 0 {
		b.pushUndo()
		prev := b.LineText(b.Cur.Line - 1)
		cur := b.LineText(b.Cur.Line)
		b.Lines[b.Cur.Line-1] = prev + cur
		b.Lines = append(b.Lines[:b.Cur.Line], b.Lines[b.Cur.Line+1:]...)
		b.Cur.Line--
		b.Cur.Col = len([]rune(prev))
		b.markDirty()
	}
}

// DeleteChar removes the rune at the cursor.
func (b *Buffer) DeleteChar() {
	b.clamp()
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	if b.Cur.Col < len(runes) {
		b.pushUndo()
		runes = append(runes[:b.Cur.Col], runes[b.Cur.Col+1:]...)
		b.Lines[b.Cur.Line] = string(runes)
		b.markDirty()
	}
}

// DeleteLine removes the whole line; returns it (for yank).
func (b *Buffer) DeleteLine() string {
	b.pushUndo()
	line := b.LineText(b.Cur.Line)
	b.Lines = append(b.Lines[:b.Cur.Line], b.Lines[b.Cur.Line+1:]...)
	if len(b.Lines) == 0 {
		b.Lines = []string{""}
	}
	b.Cur.Col = 0
	if b.Cur.Line >= len(b.Lines) {
		b.Cur.Line = len(b.Lines) - 1
	}
	b.markDirty()
	return line
}

// DeleteWord removes the word at the cursor (dw).
func (b *Buffer) DeleteWord() {
	b.clamp()
	b.pushUndo()
	line := b.LineText(b.Cur.Line)
	runes := []rune(line)
	end := wordEnd(runes, b.Cur.Col)
	if end > b.Cur.Col {
		runes = append(runes[:b.Cur.Col], runes[end:]...)
		b.Lines[b.Cur.Line] = string(runes)
		b.markDirty()
	}
}

// InsertLineBefore/After insert an empty line and move there.
func (b *Buffer) InsertLineBefore() {
	b.pushUndo()
	b.Lines = append(b.Lines, "")
	copy(b.Lines[b.Cur.Line+1:], b.Lines[b.Cur.Line:])
	b.Lines[b.Cur.Line] = ""
	b.Cur.Col = 0
	b.markDirty()
}

func (b *Buffer) InsertLineAfter() {
	b.pushUndo()
	b.Lines = append(b.Lines, "")
	copy(b.Lines[b.Cur.Line+2:], b.Lines[b.Cur.Line+1:])
	b.Lines[b.Cur.Line+1] = ""
	b.Cur.Line++
	b.Cur.Col = 0
	b.markDirty()
}

// Undo restores the previous snapshot.
func (b *Buffer) Undo() bool {
	if len(b.UndoStack) == 0 {
		return false
	}
	last := b.UndoStack[len(b.UndoStack)-1]
	b.UndoStack = b.UndoStack[:len(b.UndoStack)-1]
	b.RedoStack = append(b.RedoStack, Snapshot{Lines: append([]string(nil), b.Lines...), Cur: b.Cur})
	b.Lines = last.Lines
	b.Cur = last.Cur
	b.clamp()
	b.Dirty = true
	return true
}

// Redo restores the next snapshot.
func (b *Buffer) Redo() bool {
	if len(b.RedoStack) == 0 {
		return false
	}
	last := b.RedoStack[len(b.RedoStack)-1]
	b.RedoStack = b.RedoStack[:len(b.RedoStack)-1]
	b.UndoStack = append(b.UndoStack, Snapshot{Lines: append([]string(nil), b.Lines...), Cur: b.Cur})
	b.Lines = last.Lines
	b.Cur = last.Cur
	b.clamp()
	b.Dirty = true
	return true
}

// SetMark records a mark at the cursor.
func (b *Buffer) SetMark(name byte) {
	b.Marks[name] = b.Cur
}

// JumpMark moves to a mark.
func (b *Buffer) JumpMark(name byte) bool {
	pos, ok := b.Marks[name]
	if !ok {
		return false
	}
	b.Cur = pos
	b.clamp()
	return true
}

// WriteFile saves the buffer to disk and clears the dirty flag.
func (b *Buffer) WriteFile() error {
	if err := os.WriteFile(b.Path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", b.Path, err)
	}
	b.Dirty = false
	return nil
}

// SaveSnapshot returns a serializable state (crash recovery, detach).
func (b *Buffer) SaveSnapshot() Snapshot {
	return Snapshot{Lines: append([]string(nil), b.Lines...), Cur: b.Cur}
}

// RestoreSnapshot replaces the state (crash recovery, attach).
func (b *Buffer) RestoreSnapshot(s Snapshot) {
	b.Lines = append([]string(nil), s.Lines...)
	b.Cur = s.Cur
	b.clamp()
	b.Dirty = true
}

// wordEnd finds the end of the word starting at col.
func wordEnd(runes []rune, col int) int {
	n := len(runes)
	if col >= n {
		return n
	}
	i := col
	// skip word chars
	for i < n && isWordRune(runes[i]) {
		i++
	}
	if i == col { // at a non-word char: skip to next word char
		for i < n && !isWordRune(runes[i]) && runes[i] != ' ' {
			i++
		}
	}
	return i
}

func isWordRune(r rune) bool {
	return r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
