package editor

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

// Palette colors for the editor UI (Charmtone).
type Palette struct {
	Accent, CursorInk, Fg, FgSubtle, FgVerySubtle, Selection, Success, Warning, Error, Comment, String, Keyword, Type color.Color
}

// Charmtone returns the editor palette.
func Charmtone() Palette {
	return Palette{
		Accent:       lipgloss.Color("#FF6363"),
		CursorInk:    lipgloss.Color("#090B0F"),
		Fg:           lipgloss.Color("#D9DCDA"),
		FgSubtle:     lipgloss.Color("#929795"),
		FgVerySubtle: lipgloss.Color("#34393A"),
		Selection:    lipgloss.Color("#352E4E"),
		Success:      lipgloss.Color("#76B852"),
		Warning:      lipgloss.Color("#DDB642"),
		Error:        lipgloss.Color("#F05D57"),
		Comment:      lipgloss.Color("#666D70"),
		String:       lipgloss.Color("#86B85C"),
		Keyword:      lipgloss.Color("#C9A7EB"),
		Type:         lipgloss.Color("#DDB642"),
	}
}

// Palettes is the theme browser catalog (Space t, §5.2.3).
var Palettes = map[string]Palette{
	"charmtone": {
		Accent: lipgloss.Color("#FF6363"), CursorInk: lipgloss.Color("#090B0F"), Fg: lipgloss.Color("#D9DCDA"),
		FgSubtle: lipgloss.Color("#929795"), FgVerySubtle: lipgloss.Color("#34393A"), Selection: lipgloss.Color("#352E4E"),
		Success: lipgloss.Color("#76B852"), Warning: lipgloss.Color("#DDB642"),
		Error: lipgloss.Color("#F05D57"), Comment: lipgloss.Color("#666D70"),
		String: lipgloss.Color("#86B85C"), Keyword: lipgloss.Color("#C9A7EB"),
		Type: lipgloss.Color("#DDB642"),
	},
	"nord": {
		Accent: lipgloss.Color("#88C0D0"), CursorInk: lipgloss.Color("#242933"), Fg: lipgloss.Color("#D8DEE9"),
		FgSubtle: lipgloss.Color("#81A1C1"), FgVerySubtle: lipgloss.Color("#4C566A"), Selection: lipgloss.Color("#3B4B5A"),
		Success: lipgloss.Color("#A3BE8C"), Warning: lipgloss.Color("#EBCB8B"),
		Error: lipgloss.Color("#BF616A"), Comment: lipgloss.Color("#616E88"),
		String: lipgloss.Color("#A3BE8C"), Keyword: lipgloss.Color("#81A1C1"),
		Type: lipgloss.Color("#B48EAD"),
	},
	"gruvbox": {
		Accent: lipgloss.Color("#FB4934"), CursorInk: lipgloss.Color("#1D2021"), Fg: lipgloss.Color("#EBDBB2"),
		FgSubtle: lipgloss.Color("#A89984"), FgVerySubtle: lipgloss.Color("#504945"), Selection: lipgloss.Color("#5A3C35"),
		Success: lipgloss.Color("#B8BB26"), Warning: lipgloss.Color("#FABD2F"),
		Error: lipgloss.Color("#FB4934"), Comment: lipgloss.Color("#928374"),
		String: lipgloss.Color("#B8BB26"), Keyword: lipgloss.Color("#FB4934"),
		Type: lipgloss.Color("#D3869B"),
	},
	"one-dark": {
		Accent: lipgloss.Color("#61AFEF"), CursorInk: lipgloss.Color("#21252B"), Fg: lipgloss.Color("#ABB2BF"),
		FgSubtle: lipgloss.Color("#7F848E"), FgVerySubtle: lipgloss.Color("#3E4451"), Selection: lipgloss.Color("#33445A"),
		Success: lipgloss.Color("#98C379"), Warning: lipgloss.Color("#E5C07B"),
		Error: lipgloss.Color("#E06C75"), Comment: lipgloss.Color("#5C6370"),
		String: lipgloss.Color("#98C379"), Keyword: lipgloss.Color("#C678DD"),
		Type: lipgloss.Color("#E5C07B"),
	},
}

// ThemeNames returns the theme catalog keys.
func ThemeNames() []string {
	names := make([]string, 0, len(Palettes))
	for n := range Palettes {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// SetPalette switches the UI palette (theme browser).
func (u *UI) SetPalette(name string) bool {
	p, ok := Palettes[name]
	if !ok {
		return false
	}
	u.Pal = p
	u.rebuildStyles()
	if u.Ed != nil {
		u.Ed.Review.Pal = p
	}
	return true
}

// SetPaletteValue applies a palette supplied by the parent TUI. This keeps
// editor chrome synchronized with themes that are not part of the editor's
// small standalone catalog (including light themes).
func (u *UI) SetPaletteValue(p Palette) {
	u.Pal = p
	u.rebuildStyles()
	if u.Ed != nil {
		u.Ed.Review.Pal = p
	}
}

// UI renders the editor inside the TUI.
type UI struct {
	Ed           *Editor
	Pal          Palette
	Width        int
	Height       int
	Highlight    Highlighter
	Gutter       *Gutter
	topLine      int // scroll offset
	manualScroll bool
	Message      string
	languagePath string
	languageHead string
	language     string
	lineCache    map[int]renderedLine
	styles       editorStyles
}

type renderedLine struct {
	path     string
	text     string
	width    int
	language string
	runes    []rune
	spans    []Span
	base     string
}

type editorStyles struct {
	byKind    map[HighlightKind]lipgloss.Style
	selection lipgloss.Style
	cursor    lipgloss.Style
	lineNum   lipgloss.Style
	activeNum lipgloss.Style
	gutter    lipgloss.Style
	rule      string
}

// NewUI builds the editor renderer.
func NewUI(ed *Editor, pal Palette) *UI {
	ui := &UI{Ed: ed, Pal: pal, Highlight: builtinHighlighter{}, lineCache: map[int]renderedLine{}}
	ui.rebuildStyles()
	ed.Review.Pal = pal
	return ui
}

func (u *UI) rebuildStyles() {
	u.styles = editorStyles{
		byKind: map[HighlightKind]lipgloss.Style{
			HlNone:    lipgloss.NewStyle().Foreground(u.Pal.Fg),
			HlComment: lipgloss.NewStyle().Foreground(u.Pal.Comment),
			HlString:  lipgloss.NewStyle().Foreground(u.Pal.String),
			HlKeyword: lipgloss.NewStyle().Foreground(u.Pal.Keyword),
			HlType:    lipgloss.NewStyle().Foreground(u.Pal.Type),
			HlFunc:    lipgloss.NewStyle().Foreground(u.Pal.Accent),
			HlNumber:  lipgloss.NewStyle().Foreground(u.Pal.Warning),
			HlTitle:   lipgloss.NewStyle().Foreground(u.Pal.Accent),
		},
		selection: lipgloss.NewStyle().Background(u.Pal.Selection).Foreground(u.Pal.Fg),
		cursor:    lipgloss.NewStyle().Background(u.Pal.Accent).Foreground(u.Pal.CursorInk),
		lineNum:   lipgloss.NewStyle().Foreground(u.Pal.FgVerySubtle),
		activeNum: lipgloss.NewStyle().Foreground(u.Pal.Accent),
		gutter:    lipgloss.NewStyle().Foreground(u.Pal.Success),
		rule:      lipgloss.NewStyle().Foreground(u.Pal.FgVerySubtle).Render("│ "),
	}
	u.lineCache = map[int]renderedLine{}
}

// ScrollOffset exposes the first visible source line for UI coordination and
// black-box interaction tests. Mouse wheel scrolling changes this value
// without moving the editor cursor.
func (u *UI) ScrollOffset() int { return u.topLine }

// KeyFromTea converts a bubbletea key into an editor key.
func KeyFromTea(msg tea.KeyMsg) Key {
	switch msg.Type {
	case tea.KeyEsc:
		return Key{Kind: KeyEsc}
	case tea.KeyEnter:
		return Key{Kind: KeyEnter}
	case tea.KeyBackspace:
		return Key{Kind: KeyBackspace}
	case tea.KeyDelete:
		return Key{Kind: KeyDelete}
	case tea.KeyLeft:
		return Key{Kind: KeyLeft}
	case tea.KeyRight:
		return Key{Kind: KeyRight}
	case tea.KeyUp:
		return Key{Kind: KeyUp}
	case tea.KeyDown:
		return Key{Kind: KeyDown}
	case tea.KeyHome:
		return Key{Kind: KeyHome}
	case tea.KeyEnd:
		return Key{Kind: KeyEnd}
	case tea.KeyShiftLeft:
		return Key{Kind: KeyLeft, Shift: true}
	case tea.KeyShiftRight:
		return Key{Kind: KeyRight, Shift: true}
	case tea.KeyShiftUp:
		return Key{Kind: KeyUp, Shift: true}
	case tea.KeyShiftDown:
		return Key{Kind: KeyDown, Shift: true}
	case tea.KeyShiftHome:
		return Key{Kind: KeyHome, Shift: true}
	case tea.KeyShiftEnd:
		return Key{Kind: KeyEnd, Shift: true}
	case tea.KeyCtrlR:
		return Key{Kind: KeyCtrlR}
	case tea.KeyCtrlS:
		return Key{Kind: KeyCtrlS, Ctrl: true}
	case tea.KeyCtrlZ:
		return Key{Kind: KeyCtrlZ, Ctrl: true}
	case tea.KeyCtrlY:
		return Key{Kind: KeyCtrlY, Ctrl: true}
	case tea.KeyCtrlA:
		return Key{Kind: KeyCtrlA, Ctrl: true}
	case tea.KeyCtrlP:
		return Key{Kind: KeyCtrlP, Ctrl: true}
	case tea.KeySpace:
		return Key{Kind: KeySpace}
	case tea.KeyTab:
		return Key{Kind: KeyTab}
	case tea.KeyRunes:
		return Key{Kind: KeyRune, Runes: msg.Runes}
	}
	return Key{Kind: KeyRune, Runes: msg.Runes}
}

// Update forwards a tea key and returns the action.
func (u *UI) Update(msg tea.KeyMsg) EditAction {
	before := Cursor{}
	if u.Ed != nil && u.Ed.Buffer() != nil {
		before = u.Ed.Buffer().Cur
	}
	action := u.Ed.Update(KeyFromTea(msg))
	if u.Ed != nil && u.Ed.Buffer() != nil && u.Ed.Buffer().Cur != before {
		// A real cursor/edit movement resumes follow-cursor behavior. A
		// wheel/PageUp scroll leaves the cursor untouched and keeps the
		// manually chosen viewport anchored.
		u.manualScroll = false
	}
	return action
}

// Paste forwards bracketed-paste text through the editor's literal paste
// path, bypassing modal key handling while retaining viewport follow.
func (u *UI) Paste(text string) EditAction {
	before := Cursor{}
	if u.Ed != nil && u.Ed.Buffer() != nil {
		before = u.Ed.Buffer().Cur
	}
	action := u.Ed.Paste(text)
	if u.Ed != nil && u.Ed.Buffer() != nil && u.Ed.Buffer().Cur != before {
		u.manualScroll = false
	}
	return action
}

// lineRender renders one source line with syntax spans, truncating the
// PLAIN text before any styling so escape sequences are never split. When
// cursorCol is >= 0, that character is highlighted with the accent
// background (the modal cursor); when it lies past the visible text an end
// marker is appended.
func (u *UI) lineRender(text string, width, cursorCol, lineNo int) string {
	plain := truncate(text, width)
	entry := u.cachedLine(lineNo, plain, width)
	selStart, selEnd, selected := u.Ed.SelectionRange(lineNo, len(entry.runes))
	if !selected {
		selStart, selEnd = -1, -1
	}
	if cursorCol < 0 && !selected {
		return entry.base
	}
	out := u.renderLineSegments(entry, selStart, selEnd, cursorCol)
	if cursorCol >= len(entry.runes) {
		out += u.styles.cursor.Render("█")
	}
	return out
}

func (u *UI) detectedLanguage() string {
	if u.Ed == nil || u.Ed.Buffer() == nil {
		return ""
	}
	b := u.Ed.Buffer()
	head := b.LineText(0)
	if u.languagePath == b.Path && u.languageHead == head {
		return u.language
	}
	language := u.Highlight.Detect(b.Path)
	if detector, ok := u.Highlight.(interface {
		DetectContent(path, firstLine string) string
	}); ok {
		language = detector.DetectContent(b.Path, head)
	}
	u.languagePath, u.languageHead, u.language = b.Path, head, language
	return language
}

func (u *UI) cachedLine(lineNo int, plain string, width int) renderedLine {
	path := ""
	if u.Ed != nil && u.Ed.Buffer() != nil {
		path = u.Ed.Buffer().Path
	}
	language := u.detectedLanguage()
	if entry, ok := u.lineCache[lineNo]; ok && entry.path == path && entry.text == plain && entry.width == width && entry.language == language {
		return entry
	}
	// Keep the cache viewport-sized even when a user scrolls through a very
	// large file. Clearing is cheap and avoids retaining every visited line.
	if len(u.lineCache) > max(u.Height*8, 512) {
		u.lineCache = map[int]renderedLine{}
	}
	entry := renderedLine{
		path: path, text: plain, width: width, language: language,
		runes: []rune(plain), spans: u.Highlight.Spans(language, plain),
	}
	entry.base = u.renderLineSegments(entry, -1, -1, -1)
	u.lineCache[lineNo] = entry
	return entry
}

func (u *UI) renderLineSegments(line renderedLine, selStart, selEnd, cursor int) string {
	length := len(line.runes)
	if length == 0 {
		return ""
	}
	boundaries := []int{0, length}
	for _, span := range line.spans {
		boundaries = append(boundaries, clampInt(span.Start, 0, length), clampInt(span.End, 0, length))
	}
	if selStart >= 0 && selEnd > selStart {
		boundaries = append(boundaries, clampInt(selStart, 0, length), clampInt(selEnd, 0, length))
	}
	if cursor >= 0 && cursor < length {
		boundaries = append(boundaries, cursor, cursor+1)
	}
	sort.Ints(boundaries)
	var out strings.Builder
	last := -1
	for i := 0; i+1 < len(boundaries); i++ {
		start, end := boundaries[i], boundaries[i+1]
		if start == last || start >= end {
			continue
		}
		last = start
		style := u.styles.byKind[u.highlightKindAt(line.spans, start)]
		if start >= selStart && start < selEnd {
			style = u.styles.selection
		}
		if start == cursor {
			style = u.styles.cursor
		}
		out.WriteString(style.Render(string(line.runes[start:end])))
	}
	return out.String()
}

func (u *UI) highlightKindAt(spans []Span, pos int) HighlightKind {
	for _, span := range spans {
		if pos >= span.Start && pos < span.End {
			return span.Kind
		}
	}
	return HlNone
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

// View renders the editor canvas. Workspace metadata belongs to the TUI's
// single global statusline; duplicating it here made the code area cramped
// and repeated the absolute path, mode and language twice.
func (u *UI) View() string {
	b := u.Ed.Buffer()
	if b == nil {
		return "no buffer"
	}
	width := u.Width - 6 // gutter + numbers
	if width < 10 {
		width = 10
	}
	// Scroll so the cursor is visible.
	viewLines := max(u.Height, 1)
	maxTop := max(len(b.Lines)-max(viewLines, 1), 0)
	if !u.manualScroll {
		if b.Cur.Line < u.topLine {
			u.topLine = b.Cur.Line
		}
		if b.Cur.Line >= u.topLine+viewLines {
			u.topLine = b.Cur.Line - viewLines + 1
		}
	}
	u.topLine = max(0, min(u.topLine, maxTop))

	var out strings.Builder
	for i := u.topLine; i < len(b.Lines) && i < u.topLine+viewLines; i++ {
		lineNum := fmt.Sprintf("%3d", i+1)
		numStyle := u.styles.lineNum
		if i == b.Cur.Line {
			numStyle = u.styles.activeNum
		}
		sign := " "
		if u.Gutter != nil {
			switch u.Gutter.Signs[i+1] {
			case SignAdded:
				sign = "+"
			case SignModified:
				sign = "~"
			case SignDeleted:
				sign = "-"
			}
		}
		cursor := -1
		if i == b.Cur.Line {
			cursor = b.Cur.Col
		}
		line := u.lineRender(b.LineText(i), width, cursor, i)
		out.WriteString(numStyle.Render(lineNum) + u.styles.gutter.Render(sign) + u.styles.rule + line + "\n")
	}
	// Empty canvas rows stay empty. Vim-style tildes add visual noise in a
	// three-pane IDE and were absent from the approved design.
	for i := len(b.Lines) - u.topLine; i < viewLines; i++ {
		out.WriteString("\n")
	}
	return strings.TrimSuffix(out.String(), "\n")
}

// truncate projects one source line to terminal-safe text and cuts it to w
// runes. Source buffers intentionally retain their original bytes for editing
// and saving, but those bytes are never trusted as terminal output: C0/C1
// controls become one-cell visible glyphs and malformed UTF-8 becomes U+FFFD.
//
// Work is bounded by the viewport width. A generated file with a multi-MB
// line must not be decoded or highlighted in full merely to paint one frame.
// It must only be called on unstyled text: cutting styled output would split
// the renderer's own escape sequences.
func truncate(s string, w int) string {
	if w <= 0 || s == "" {
		return ""
	}
	// Never trust a synthetic/window-size width as an allocation size. A
	// source rune occupies at least one byte, so len(s) is a safe upper bound.
	runes := make([]rune, 0, min(w, len(s)))
	for len(s) > 0 && len(runes) < w {
		r, size := utf8.DecodeRuneInString(s)
		if r == utf8.RuneError && size == 1 {
			// Consume exactly one malformed byte so the projection remains
			// valid UTF-8 and cursor columns remain one-for-one.
			r = utf8.RuneError
		}
		s = s[size:]
		switch {
		case r < 0x20:
			r = 0x2400 + r // Unicode Control Pictures: NUL→␀, ESC→␛.
		case r == 0x7f:
			r = 0x2421 // DEL → ␡.
		case r >= 0x80 && r <= 0x9f:
			r = utf8.RuneError // C1 includes the 8-bit CSI/OSC controls.
		case isBidiFormatControl(r):
			r = utf8.RuneError // Prevent visual reordering/Trojan Source output.
		}
		runes = append(runes, r)
	}
	if s == "" {
		return string(runes)
	}
	return string(runes[:max(w-1, 0)]) + "…"
}

func isBidiFormatControl(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f ||
		(r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069)
}

// RenderOverlay renders picker/review/git overlays.
func (u *UI) RenderOverlay() string {
	e := u.Ed
	switch {
	case e.Picker.Active:
		return e.Picker.View(u.Width - 4)
	case e.Review.Active:
		return e.Review.View(u.Width - 4)
	case e.Git != nil && e.Git.Active:
		return e.Git.View(u.Width - 4)
	}
	return ""
}

// SetScroll resets the viewport to an explicit position and resumes normal
// follow-cursor behavior on the next render.
func (u *UI) SetScroll(top int) {
	u.topLine = max(top, 0)
	u.manualScroll = false
}

// Scroll moves the visible editor window without changing the cursor.
func (u *UI) Scroll(delta int) {
	if u.Ed == nil || u.Ed.Buffer() == nil {
		return
	}
	maxTop := max(len(u.Ed.Buffer().Lines)-max(u.Height, 1), 0)
	u.topLine = max(0, min(u.topLine+delta, maxTop))
	u.manualScroll = true
}

// CursorAt maps editor content coordinates to a buffer cursor. The editor
// gutter is 6 cells wide (line number + sign), so text starts at column 6.
func (u *UI) CursorAt(x, y int) Cursor {
	if u.Ed == nil || u.Ed.Buffer() == nil {
		return Cursor{}
	}
	line := u.topLine + max(y, 0)
	if maxLine := max(len(u.Ed.Buffer().Lines)-1, 0); line > maxLine {
		line = maxLine
	}
	col := max(x-6, 0)
	if maxCol := len([]rune(u.Ed.Buffer().LineText(line))); col > maxCol {
		col = maxCol
	}
	return Cursor{Line: line, Col: col}
}
