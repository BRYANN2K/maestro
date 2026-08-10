package editor

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/proposals"
)

func testBuffer(t *testing.T, content string) *Buffer {
	t.Helper()
	return NewBuffer("test.go", []byte(content))
}

func TestBufferBasics(t *testing.T) {
	b := testBuffer(t, "package main\n\nfunc main() {}\n")
	if b.NumLines() != 3 {
		t.Fatalf("lines = %d", b.NumLines())
	}
	if b.LineText(0) != "package main" {
		t.Errorf("line0 = %q", b.LineText(0))
	}
	b.Cur = Cursor{Line: 0, Col: 7} // the space after "package"
	b.InsertText("X")
	if b.LineText(0) != "packageX main" {
		t.Errorf("after insert = %q", b.LineText(0))
	}
	if !b.Dirty {
		t.Error("buffer should be dirty")
	}
	b.Backspace() // cursor is after X
	if b.LineText(0) != "package main" {
		t.Errorf("after backspace = %q", b.LineText(0))
	}
}

func TestUndoRedo(t *testing.T) {
	b := testBuffer(t, "one\ntwo\n")
	b.Cur = Cursor{Line: 1, Col: 3}
	b.InsertText("!")
	if !b.Undo() {
		t.Fatal("undo failed")
	}
	if b.LineText(1) != "two" {
		t.Errorf("after undo = %q", b.LineText(1))
	}
	if !b.Redo() {
		t.Fatal("redo failed")
	}
	if b.LineText(1) != "two!" {
		t.Errorf("after redo = %q", b.LineText(1))
	}
}

func TestInsertNewlineAndJoin(t *testing.T) {
	b := testBuffer(t, "ab\n")
	b.Cur = Cursor{Line: 0, Col: 1}
	b.InsertNewline()
	if b.NumLines() != 2 || b.LineText(0) != "a" || b.LineText(1) != "b" {
		t.Fatalf("after newline: %q / %q", b.LineText(0), b.LineText(1))
	}
	// Backspace joins lines.
	b.Backspace()
	if b.NumLines() != 1 || b.LineText(0) != "ab" {
		t.Errorf("after join = %q", b.LineText(0))
	}
}

func TestCursorColumnUsesRunesAcrossLines(t *testing.T) {
	b := testBuffer(t, "abcdefgh\né界\n")
	b.Cur = Cursor{Line: 0, Col: 6}
	b.Move(MotDown, 1)
	if want := (Cursor{Line: 1, Col: 2}); b.Cur != want {
		t.Fatalf("cursor after moving to Unicode line = %+v, want %+v", b.Cur, want)
	}

	// This used to panic because clamp compared a rune column with the
	// UTF-8 byte length of the destination line.
	b.InsertRune('!')
	if got := b.LineText(1); got != "é界!" {
		t.Fatalf("Unicode insert = %q, want %q", got, "é界!")
	}
}

func TestInsertTextIsAtomicAndPreservesNewlinesAndTabs(t *testing.T) {
	b := testBuffer(t, "tail\n")
	b.InsertText("α\t\nβ")
	if got := b.Lines; len(got) != 2 || got[0] != "α\t" || got[1] != "βtail" {
		t.Fatalf("inserted lines = %#v", got)
	}
	if want := (Cursor{Line: 1, Col: 1}); b.Cur != want {
		t.Fatalf("cursor = %+v, want %+v", b.Cur, want)
	}
	if got := len(b.UndoStack); got != 1 {
		t.Fatalf("undo entries = %d, want one transaction", got)
	}
	if !b.Undo() || b.LineText(0) != "tail" || b.NumLines() != 1 {
		t.Fatalf("single undo did not restore buffer: %#v", b.Lines)
	}
}

func TestDeleteLineAndPaste(t *testing.T) {
	b := testBuffer(t, "a\nb\nc\n")
	b.Cur = Cursor{Line: 1, Col: 0}
	line := b.DeleteLine()
	if line != "b" || b.NumLines() != 2 {
		t.Fatalf("delete = %q lines %d", line, b.NumLines())
	}
}

func TestMotions(t *testing.T) {
	cases := []struct {
		name   string
		motion Motion
		from   Cursor
		want   Cursor
	}{
		{"left", MotLeft, Cursor{0, 5}, Cursor{0, 4}},
		{"right", MotRight, Cursor{0, 4}, Cursor{0, 5}},
		{"down", MotDown, Cursor{0, 0}, Cursor{1, 0}},
		{"up", MotUp, Cursor{1, 0}, Cursor{0, 0}},
		{"word forward", MotWordForward, Cursor{0, 0}, Cursor{0, 6}},
		{"word forward again", MotWordForward, Cursor{0, 6}, Cursor{0, 12}},
		{"word back", MotWordBack, Cursor{0, 12}, Cursor{0, 6}},
		{"word end", MotWordEnd, Cursor{0, 0}, Cursor{0, 4}},
		{"line start", MotLineStart, Cursor{0, 9}, Cursor{0, 0}},
		{"line end", MotLineEnd, Cursor{0, 0}, Cursor{0, 14}},
		{"doc start", MotDocStart, Cursor{1, 3}, Cursor{0, 0}},
		{"doc end", MotDocEnd, Cursor{0, 0}, Cursor{1, 6}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := testBuffer(t, "hello world foo\nbar baz\n")
			b.Cur = tc.from
			b.Move(tc.motion, 1)
			if b.Cur != tc.want {
				t.Errorf("cur = %+v, want %+v", b.Cur, tc.want)
			}
		})
	}
}

func TestCountedMotions(t *testing.T) {
	b := testBuffer(t, "a b c d e\n")
	b.Cur = Cursor{0, 0}
	b.Move(MotWordForward, 3)
	if b.Cur.Col != 6 {
		t.Errorf("3w col = %d, want 6", b.Cur.Col)
	}
}

func TestMatchPair(t *testing.T) {
	b := testBuffer(t, "func f(x) { return (a + b) }\n")
	b.Cur = Cursor{0, 6} // the "(" of (x)
	if !b.Move(MotMatchPair, 1) {
		t.Fatal("no match")
	}
	// forward: (x) → its closing paren
	if b.Cur.Col != 8 {
		t.Errorf("forward matched col = %d, want 8", b.Cur.Col)
	}
	// backward: from ")" → "("
	if !b.Move(MotMatchPair, 1) {
		t.Fatal("no backward match")
	}
	if b.Cur.Col != 6 {
		t.Errorf("backward matched col = %d, want 6", b.Cur.Col)
	}
	// nested: "(" of (a + b) → its ")"
	b.Cur = Cursor{0, 19}
	if !b.Move(MotMatchPair, 1) || b.Cur.Col != 25 {
		t.Errorf("nested matched col = %d, want 25", b.Cur.Col)
	}
	// no pair
	b2 := testBuffer(t, "plain text\n")
	b2.Cur = Cursor{0, 0}
	if b2.Move(MotMatchPair, 1) {
		t.Error("no-pair should fail")
	}
}

func TestFindChar(t *testing.T) {
	b := testBuffer(t, "hello world\n")
	b.Cur = Cursor{0, 0}
	if !b.FindChar('o') {
		t.Fatal("find o failed")
	}
	if b.Cur.Col != 4 {
		t.Errorf("col = %d", b.Cur.Col)
	}
	b2 := testBuffer(t, "abc\n")
	b2.Cur = Cursor{0, 0}
	if b2.ToChar('c') {
		if b2.Cur.Col != 1 {
			t.Errorf("t c col = %d", b2.Cur.Col)
		}
	} else {
		t.Error("t c failed")
	}
}

func TestMarks(t *testing.T) {
	b := testBuffer(t, "one\ntwo\nthree\n")
	b.Cur = Cursor{2, 0}
	b.SetMark('a')
	b.Cur = Cursor{0, 0}
	if !b.JumpMark('a') {
		t.Fatal("jump failed")
	}
	if b.Cur.Line != 2 {
		t.Errorf("jumped to line %d", b.Cur.Line)
	}
	if b.JumpMark('z') {
		t.Error("unknown mark should fail")
	}
}

func TestSearch(t *testing.T) {
	b := testBuffer(t, "hello world\nfoo hello bar\n")
	b.Cur = Cursor{0, 0}
	pos, ok := b.FindOnLine("hello")
	if !ok || pos.Line != 0 || pos.Col != 0 {
		t.Fatalf("first search = %+v, %v", pos, ok)
	}
	// search from after the first match
	b.Cur = Cursor{0, 1}
	pos, ok = b.FindOnLine("hello")
	if !ok || pos.Line != 1 || pos.Col != 4 {
		t.Errorf("second search = %+v, %v", pos, ok)
	}
}

func TestVisualYank(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	e := NewEditor(dir)
	if err := e.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	buf := e.Buffer()
	buf.Cur = Cursor{0, 0}
	e.startVisual(buf, true, false)
	buf.Cur = Cursor{2, 0}
	e.yankVisual(buf)
	if len(e.yanked) != 3 || e.yanked[0] != "alpha" || e.yanked[2] != "gamma" {
		t.Errorf("yanked = %v", e.yanked)
	}
}

func TestEditorNormalKeys(t *testing.T) {
	e := NewEditor("")
	e.SetKeymap("vim")
	b := testBuffer(t, "abc def\n")
	e.Buffers = append(e.Buffers, b)
	e.CurBuf = 0
	buf := e.Buffer()
	buf.Cur = Cursor{0, 0}

	// "w" moves to the second word.
	e.Update(RuneKey('w'))
	if buf.Cur.Col != 4 {
		t.Errorf("w col = %d", buf.Cur.Col)
	}
	// "0" to start.
	e.Update(RuneKey('0'))
	if buf.Cur.Col != 0 {
		t.Errorf("0 col = %d", buf.Cur.Col)
	}
	// "3l"
	e.Update(RuneKey('3'))
	e.Update(RuneKey('l'))
	if buf.Cur.Col != 3 {
		t.Errorf("3l col = %d", buf.Cur.Col)
	}
	// "i" enters insert, typing works.
	e.Update(RuneKey('i'))
	if e.Mode != ModeInsert {
		t.Fatalf("mode = %v", e.Mode)
	}
	e.Update(RuneKey('X'))
	if buf.LineText(0) != "abcX def" {
		t.Errorf("insert = %q", buf.LineText(0))
	}
	e.Update(Key{Kind: KeyEsc})
	if e.Mode != ModeNormal {
		t.Errorf("esc mode = %v", e.Mode)
	}
	// "dd" deletes the line.
	e.Update(RuneKey('d'))
	e.Update(RuneKey('d'))
	if buf.NumLines() != 1 || buf.LineText(0) != "" {
		t.Errorf("dd = %q (%d lines)", buf.LineText(0), buf.NumLines())
	}
}

func TestStandardEditorSelectionCanReplaceByTyping(t *testing.T) {
	e := NewEditor("")
	e.Buffers = []*Buffer{NewBuffer("notes.txt", []byte("hello world\n"))}
	e.CurBuf = 0
	b := e.Buffer()
	b.Cur = Cursor{Line: 0, Col: 6}

	// Shift+arrow is the familiar non-modal selection gesture.
	e.Update(Key{Kind: KeyRight, Shift: true})
	e.Update(Key{Kind: KeyRight, Shift: true})
	if text, _, _, ok := e.SelectionText(); !ok || text != "wo" {
		t.Fatalf("selection = %q, ok=%v; want %q", text, ok, "wo")
	}
	e.Update(TextKey("you"))
	if got := b.LineText(0); got != "hello yourld" {
		t.Fatalf("replacement = %q", got)
	}
	if e.HasSelection() {
		t.Fatal("selection should clear after direct replacement")
	}
	if !b.Undo() || b.LineText(0) != "hello world" {
		t.Fatalf("replacement should be one undo step: %q", b.LineText(0))
	}
}

func TestCommandMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	e := NewEditor(dir)
	if err := e.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	buf := e.Buffer()
	buf.Cur = Cursor{0, 0}
	buf.InsertText("X")
	// :w
	e.Mode = ModeCommand
	e.cmdLine = "w"
	act := e.runCommand("w")
	if act != ActSave {
		t.Fatalf("act = %v", act)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "Xhello\n" {
		t.Errorf("file = %q", data)
	}
	if buf.Dirty {
		t.Error("buffer should be clean after save")
	}
	// :q
	if e.runCommand("q") != ActQuitIDE {
		t.Error(":q should quit the IDE")
	}
	// :e missing
	e.Mode = ModeCommand
	e.runCommand("e nope.txt")
	if e.Status == "" || strings.Contains(e.Status, "opened") {
		t.Errorf("status = %q", e.Status)
	}
}

func TestAgentReviewHunks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target.go")
	if err := os.WriteFile(path, []byte("line1\nline2\nline3\nline4\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	store := proposals.NewProposalStore(filepath.Join(dir, ".proposals"))
	prop, err := store.Stage(path, "line1\nCHANGED2\nline3\nCHANGED4\n")
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if len(prop.Hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(prop.Hunks))
	}

	e := NewEditor(dir)
	e.ProposalSrc = func() []ReviewProposal {
		p, _ := store.Load(prop.ID)
		return []ReviewProposal{{Prop: p, Store: store}}
	}
	e.Review.Refresh(e.ProposalSrc)
	e.Review.Active = true

	// Reject the first hunk (index 0), accept the second.
	e.Review.Update(RuneKey('r')) // reject hunk 0
	if len(e.Review.Items[0].Prop.Hunks) != 1 {
		t.Fatalf("hunks after reject = %d", len(e.Review.Items[0].Prop.Hunks))
	}
	e.Review.Update(RuneKey('a')) // accept hunk 0 (now the second hunk)
	if len(e.Review.Items) != 0 {
		t.Fatalf("items after full accept = %d", len(e.Review.Items))
	}
	data, _ := os.ReadFile(path)
	// Rejected hunk 0 (line2 unchanged), accepted hunk 1 (line4 → CHANGED4).
	if string(data) != "line1\nline2\nline3\nCHANGED4\n" {
		t.Errorf("file after 1-reject-1-accept = %q", data)
	}
}

func TestCrashRecovery(t *testing.T) {
	dir := t.TempDir()
	crash := NewCrashStore(filepath.Join(dir, "crash"))
	e := NewEditor(dir)
	b := testBuffer(t, "dirty content\n")
	b.Dirty = true
	e.Buffers = append(e.Buffers, b)
	if err := crash.Save(e); err != nil {
		t.Fatalf("Save: %v", err)
	}
	states, err := crash.Restore()
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(states) != 1 || states[0].Lines[0] != "dirty content" {
		t.Fatalf("states = %+v", states)
	}
	// Restore is one-shot.
	states2, _ := crash.Restore()
	if len(states2) != 0 {
		t.Errorf("crash file should be consumed: %+v", states2)
	}
	// And the editor can take them back.
	e2 := NewEditor(dir)
	e2.RestoreBuffers(states)
	if e2.Buffer().LineText(0) != "dirty content" || !e2.Buffer().Dirty {
		t.Errorf("restored buffer = %q dirty %v", e2.Buffer().LineText(0), e2.Buffer().Dirty)
	}
}

func TestSessionDetachAttach(t *testing.T) {
	dir := t.TempDir()
	sess := NewSessionStore(filepath.Join(dir, "sessions"))
	e := NewEditor(dir)
	b := testBuffer(t, "attached state\n")
	b.Cur = Cursor{0, 5}
	b.Dirty = true
	e.Buffers = append(e.Buffers, b)
	path, err := sess.Save(e)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file: %v", err)
	}
	e2 := NewEditor(dir)
	ok, err := sess.Load(e2)
	if err != nil || !ok {
		t.Fatalf("Load: %v, %v", ok, err)
	}
	if e2.Buffer().LineText(0) != "attached state" || e2.Buffer().Cur.Col != 5 || !e2.Buffer().Dirty {
		t.Errorf("attached buffer = %q cur %+v dirty %v", e2.Buffer().LineText(0), e2.Buffer().Cur, e2.Buffer().Dirty)
	}
}

func TestPickerFuzzy(t *testing.T) {
	p := NewPicker()
	p.Items = []string{"internal/spec/spec.go", "internal/git/git.go", "cmd/maestro/main.go"}
	p.Query = "spec"
	got := p.Filter()
	if len(got) != 1 || got[0] != "internal/spec/spec.go" {
		t.Errorf("filter = %v", got)
	}
	p.Query = "go"
	if len(p.Filter()) != 3 {
		t.Errorf("go filter = %v", p.Filter())
	}
	p.Query = "zzz"
	if len(p.Filter()) != 0 {
		t.Errorf("zzz filter = %v", p.Filter())
	}
}

func TestSymbols(t *testing.T) {
	b := testBuffer(t, "package x\n\nfunc Hello() {}\n\nfunc main() {}\n")
	syms := Symbols(b)
	if len(syms) != 2 || !strings.Contains(syms[0], "Hello") || !strings.Contains(syms[0], "line 3") {
		t.Errorf("symbols = %v", syms)
	}
}

func TestHighlighter(t *testing.T) {
	h := builtinHighlighter{}
	if h.Detect("x.go") != "go" || h.Detect("x.md") != "markdown" || h.Detect("x.mdx") != "markdown" || h.Detect("x.xyz") != "" {
		t.Error("detect failed")
	}
	if h.DetectContent("script", "#!/usr/bin/env python3") != "python" {
		t.Error("shebang detection failed")
	}
	spans := h.Spans("go", "func main() { // comment }")
	kinds := map[HighlightKind]bool{}
	for _, sp := range spans {
		kinds[sp.Kind] = true
	}
	if !kinds[HlComment] || !kinds[HlKeyword] {
		t.Errorf("spans missing kinds: %v", spans)
	}
}

func TestEditorOpenFocus(t *testing.T) {
	e := NewEditor(t.TempDir())
	if err := e.Open("a.go"); err == nil {
		t.Fatal("opening a missing file should fail")
	}
	path := filepath.Join(t.TempDir(), "a.go")
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := e.Open(path); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := e.Open(path); err != nil {
		t.Fatalf("Open again: %v", err)
	}
	if len(e.Buffers) != 1 {
		t.Errorf("buffers = %d, want 1 (dedup)", len(e.Buffers))
	}
}

func TestEditorRejectsControlRunes(t *testing.T) {
	b := testBuffer(t, "hello\n")
	e := NewEditor(".")
	e.Buffers = []*Buffer{b}
	e.CurBuf = 0
	e.Mode = ModeInsert
	// Embedded terminal control bytes (paste-like input) never become
	// file content; printable text still works.
	e.Update(TextKey("\x1b[<35;10;10M"))
	if b.LineText(0) != "hello" {
		t.Fatalf("control runes inserted into the buffer: %q", b.LineText(0))
	}
	e.Update(TextKey("!"))
	if b.LineText(0) != "!hello" {
		t.Fatalf("printable text missing: %q", b.LineText(0))
	}
}

func TestEditorBracketedPasteKeepsPickerQuerySingleLine(t *testing.T) {
	e := NewEditor(".")
	e.Picker.Start("Files", []string{"a 界"}, nil)

	e.Paste("a\r\n界\t\x1b[31m\x07\u009b")

	if got, want := e.Picker.Query, "a 界 "; got != want {
		t.Fatalf("picker paste = %q, want %q", got, want)
	}
	if !e.Picker.Active {
		t.Fatal("pasted text must not activate or close the picker")
	}
}

var brokenSeqRe = regexp.MustCompile(`\x1b\[[0-9;?]*$|\x1b$`)

func TestEditorViewNeverSplitsANSI(t *testing.T) {
	// A Go line produces syntax spans; the cursor sits mid-keyword, and the
	// line is wider than the viewport — the three cases that used to split
	// escape sequences by slicing styled output.
	b := NewBuffer("main.go", []byte("package main\nfunc main() { println(\"hi\") }\n"))
	e := NewEditor(".")
	e.Buffers = []*Buffer{b}
	e.CurBuf = 0
	b.Cur = Cursor{Line: 0, Col: 5}
	ui := NewUI(e, Charmtone())
	ui.Width = 20
	ui.Height = 8
	view := ui.View()
	for i, line := range strings.Split(view, "\n") {
		if brokenSeqRe.MatchString(line) {
			t.Fatalf("line %d ends with a broken escape sequence: %q", i, line)
		}
	}

	// Cursor past the end of a short line renders an end marker, still with
	// intact sequences.
	b2 := NewBuffer("main.go", []byte("ok\n"))
	e2 := NewEditor(".")
	e2.Buffers = []*Buffer{b2}
	e2.CurBuf = 0
	b2.Cur = Cursor{Line: 0, Col: 2}
	ui2 := NewUI(e2, Charmtone())
	ui2.Width = 30
	ui2.Height = 5
	for i, line := range strings.Split(ui2.View(), "\n") {
		if brokenSeqRe.MatchString(line) {
			t.Fatalf("cursor-past-end line %d broken: %q", i, line)
		}
	}
}

func TestEditorViewProjectsUntrustedBytesToTerminalSafeText(t *testing.T) {
	payload := append([]byte("safe\x1b]52;c;owned\x07 \x1b[2J \u202e"), 0xc2, 0x9b, 0xff)
	// Construct the buffer directly: the open-path binary guard is a separate
	// layer, and this test proves the renderer stays safe even if an untrusted
	// line reaches it through restoration or an in-memory integration.
	b := &Buffer{Path: "payload.txt", Lines: []string{string(payload)}, Marks: map[byte]Cursor{}}
	e := NewEditor(".")
	e.Buffers = []*Buffer{b}
	e.CurBuf = 0
	ui := NewUI(e, Charmtone())
	ui.Width = 80
	ui.Height = 3

	view := ui.View()
	if got := b.LineText(0); got != string(payload) {
		t.Fatalf("rendering mutated source bytes: %q", got)
	}
	if !utf8.ValidString(view) {
		t.Fatalf("rendered view is not valid UTF-8: %q", view)
	}
	for _, dangerous := range []string{"\x1b]52", "\x1b[2J", "\u009b", "\u202e"} {
		if strings.Contains(view, dangerous) {
			t.Fatalf("untrusted terminal sequence reached the rendered view: %q in %q", dangerous, view)
		}
	}
	for _, visible := range []string{"␛]52;c;owned␇", "␛[2J", "���"} {
		if !strings.Contains(view, visible) {
			t.Fatalf("safe visible projection %q missing from %q", visible, view)
		}
	}
}

func TestEditorLineProjectionIsWidthBounded(t *testing.T) {
	const width = 12
	line := strings.Repeat("x", 8<<20) + "\x1b[2J"
	got := truncate(line, width)
	if got != strings.Repeat("x", width-1)+"…" {
		t.Fatalf("bounded line = %q", got)
	}
	if len([]rune(got)) != width || strings.Contains(got, "\x1b") {
		t.Fatalf("projection violated width/safety contract: %q", got)
	}
	if got := truncate("ok", int(^uint(0)>>1)); got != "ok" {
		t.Fatalf("synthetic huge viewport changed a short line: %q", got)
	}
}

func FuzzEditorLineProjectionTerminalSafe(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("plain é界"),
		[]byte("\x1b]52;c;owned\x07\x1b[2J"),
		[]byte("left\u202eright\u2066isolate\u2069"),
		{0x00, 0x7f, 0x80, 0x9b, 0xff},
	} {
		f.Add(seed, uint16(80))
	}
	f.Fuzz(func(t *testing.T, raw []byte, rawWidth uint16) {
		width := int(rawWidth % 512)
		got := truncate(string(raw), width)
		if !utf8.ValidString(got) {
			t.Fatalf("projection is invalid UTF-8: %q", got)
		}
		if len([]rune(got)) > width {
			t.Fatalf("projection width = %d, limit = %d", len([]rune(got)), width)
		}
		for _, r := range got {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) || isBidiFormatControl(r) {
				t.Fatalf("projection retained terminal control U+%04X in %q", r, got)
			}
		}
	})
}

func TestEditorManualScrollDoesNotSnapToCursor(t *testing.T) {
	lines := make([]string, 80)
	for i := range lines {
		lines[i] = "line"
	}
	e := NewEditor("")
	e.Buffers = []*Buffer{NewBuffer("notes.txt", []byte(strings.Join(lines, "\n")))}
	e.CurBuf = 0
	ui := NewUI(e, Charmtone())
	ui.Width = 40
	ui.Height = 8
	ui.Scroll(20)
	ui.View()
	if got := ui.CursorAt(6, 0).Line; got != 20 {
		t.Fatalf("manual scroll snapped to line %d, want 20", got)
	}
	// Moving the cursor resumes the normal follow-cursor behavior.
	e.Buffer().Cur.Line = 25
	ui.Update(tea.KeyMsg{Type: tea.KeyDown})
	ui.View()
	if got := ui.CursorAt(6, ui.Height-3).Line; got < 25 {
		t.Fatalf("cursor movement did not resume follow mode: line %d", got)
	}
}
