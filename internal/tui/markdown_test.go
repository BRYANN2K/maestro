package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/proposals"
)

func teaWindowSize(t *testing.T, w, h int) tea.Msg {
	t.Helper()
	return tea.WindowSizeMsg{Width: w, Height: h}
}

func TestPrepareConcealCollapsesLongFences(t *testing.T) {
	msg := &Message{Role: "assistant"}
	text := "before\n```go\n" + strings.Repeat("line\n", 20) + "```\nafter\n```sh\necho hi\n```\n"
	out := prepareConceal(msg, text)
	if !strings.Contains(out, "[code · go · 20 lines") {
		t.Errorf("long fence not collapsed: %q", out)
	}
	if !strings.Contains(out, "echo hi") {
		t.Errorf("short fence should stay visible: %q", out)
	}
	if len(msg.concealed) != 2 {
		t.Fatalf("concealed blocks = %d, want 2", len(msg.concealed))
	}
	if msg.concealed[0].LineCount != 20 || msg.concealed[1].LineCount != 1 {
		t.Errorf("block line counts = %d,%d", msg.concealed[0].LineCount, msg.concealed[1].LineCount)
	}
	// Expansion state survives a re-conceal of the same source.
	msg.concealed[0].Expanded = true
	out2 := prepareConceal(msg, text)
	if !strings.Contains(out2, "line") || strings.Contains(out2, "[code · go") {
		t.Errorf("expanded block should render its body: %q", out2)
	}
}

func TestPrepareConcealSkipsStreaming(t *testing.T) {
	msg := &Message{Role: "assistant", busy: true}
	text := "```go\n" + strings.Repeat("line\n", 30) + "```\n"
	out := prepareConceal(msg, text)
	if !strings.Contains(out, "line") || strings.Contains(out, "[code") {
		t.Errorf("streaming message must not be concealed: %q", out)
	}
}

func TestPrepareConcealRespectsFenceMarkerAndWidth(t *testing.T) {
	for name, fence := range map[string]string{"backticks": "````", "tildes": "~~~~"} {
		t.Run(name, func(t *testing.T) {
			shorter := fence[:3]
			body := "first\n" + shorter + "\n" + strings.Repeat("large body line\n", concealLimit+2)
			text := fence + "go\n" + body + fence + "\nafter"
			msg := &Message{Role: "assistant"}
			got := prepareConceal(msg, text)
			if len(msg.concealed) != 1 || !strings.Contains(msg.concealed[0].Body, shorter) {
				t.Fatalf("parsed blocks = %+v", msg.concealed)
			}
			if !strings.Contains(got, "[code · go ·") || strings.Contains(got, "large body line") {
				t.Fatalf("long %s block leaked instead of collapsing: %q", name, got)
			}
			if !strings.Contains(got, fence+"go\n") || !strings.Contains(got, "\n"+fence+"\nafter") {
				t.Fatalf("outer %s fence changed: %q", name, got)
			}
			msg.concealed[0].Expanded = true
			if expanded := prepareConceal(msg, text); expanded != text {
				t.Fatalf("expanded %s block differs from source:\n got: %q\nwant: %q", name, expanded, text)
			}
		})
	}
}

func TestWrapLinks(t *testing.T) {
	out := wrapLinks("see https://example.com/x and [x](https://example.com/y).", "see https://example.com/x and [x](https://example.com/y).")
	if !strings.Contains(out, "\x1b]8;;https://example.com/x\x07") {
		t.Errorf("bare URL not wrapped: %q", out)
	}
	if !strings.Contains(out, "\x1b]8;;https://example.com/y\x07") {
		t.Errorf("markdown URL not wrapped: %q", out)
	}
	// Trailing punctuation is trimmed from the hyperlink target.
	out = wrapLinks("visit https://example.com/page, ok", "visit https://example.com/page, ok")
	if !strings.Contains(out, "\x1b]8;;https://example.com/page\x07") {
		t.Errorf("URL punctuation not trimmed: %q", out)
	}
}

func TestThinkingBlockRendersAndToggles(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, teaWindowSize(t, 100, 40))
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "dev", Status: "running", Detail: "building",
	})})
	last := m.lastAssistant()
	if last == nil || last.think == nil || last.think.Role != "dev" {
		t.Fatalf("thinking not attached: %+v", last)
	}
	view := m.View()
	if !strings.Contains(stripANSI(view), "thinking") {
		t.Errorf("view missing running summary: %q", stripANSI(view))
	}
	// Complete the turn: collapsed done line.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "dev", Status: "done", Detail: "build finished",
	})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})})
	view = m.View()
	clean := stripANSI(view)
	if !strings.Contains(clean, "worked") {
		t.Errorf("view missing done summary: %q", clean)
	}
	// "t" expands the summary.
	m.focus = FocusViewport
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if !last.think.Expanded {
		t.Error("t should expand the working summary")
	}
	if !strings.Contains(stripANSI(m.View()), "build finished") {
		t.Errorf("expanded summary should show detail: %q", stripANSI(m.View()))
	}
}

func TestVerbGroupingRendersCount(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, teaWindowSize(t, 100, 40))
	msg := &Message{Role: "assistant", State: "build", busy: true}
	for i := 0; i < 3; i++ {
		msg.Cards = append(msg.Cards, &Card{
			ID: fmt.Sprintf("c%d", i), Name: "read", Status: "done", Detail: "one line",
		})
	}
	m.messages = append(m.messages, msg)
	m.renderMessages()
	clean := stripANSI(m.View())
	if !strings.Contains(clean, "3 × read") {
		t.Errorf("grouped card missing: %q", clean)
	}
}

func TestDiffViewRendersGithubStyle(t *testing.T) {
	styles := NewStyles(Charmtone())
	prop := &proposals.Proposal{
		Path:      "target.txt",
		BaseLines: []string{"one", "two", "three"},
		Hunks: []proposals.Hunk{{
			Start:    1,
			OldLines: []string{"one", "two"},
			NewLines: []string{"ONE", "two"},
		}},
	}
	out := DiffView(styles, prop, 80)
	clean := stripANSI(out)
	if !strings.Contains(clean, "+2 −2") {
		t.Errorf("missing summary: %q", clean)
	}
	if !strings.Contains(clean, "@@ -1,2 +1,2 @@") {
		t.Errorf("missing hunk header: %q", clean)
	}
	if !strings.Contains(clean, "- one") || !strings.Contains(clean, "+ ONE") {
		t.Errorf("missing +/- lines: %q", clean)
	}
	// Context lines (two and three follow the hunk).
	if !strings.Contains(clean, "two") || !strings.Contains(clean, "three") {
		t.Errorf("missing context lines: %q", clean)
	}
	// Inline highlight isolates the changed middle.
	op, np := inlinePair("one", "ONE")
	if op != (inlineRange{start: 0, end: 3}) || np != (inlineRange{start: 0, end: 3}) {
		t.Errorf("inlinePair = %+v,%+v", op, np)
	}
}

func TestDiffInlinePreservesSourceTextAndRuneBoundaries(t *testing.T) {
	styles := NewStyles(Charmtone())
	bg := styles.T.Color(TokenPanel)
	tests := []struct {
		name                   string
		old, new               string
		oldChanged, newChanged string
	}{
		{name: "middle replacement", old: "abcXdef", new: "abcYdef", oldChanged: "X", newChanged: "Y"},
		{name: "Unicode replacement", old: "pré🙂fin", new: "pré界fin", oldChanged: "🙂", newChanged: "界"},
		{name: "Unicode suffix insertion", old: "café", new: "café界", newChanged: "界"},
		{name: "prefix deletion", old: "🙂alpha", new: "alpha", oldChanged: "🙂"},
		{name: "identical", old: "déjà", new: "déjà"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			oldRange, newRange := inlinePair(tc.old, tc.new)
			oldRunes, newRunes := []rune(tc.old), []rune(tc.new)
			if got := string(oldRunes[oldRange.start:oldRange.end]); got != tc.oldChanged {
				t.Fatalf("old changed segment = %q, want %q", got, tc.oldChanged)
			}
			if got := string(newRunes[newRange.start:newRange.end]); got != tc.newChanged {
				t.Fatalf("new changed segment = %q, want %q", got, tc.newChanged)
			}
			if got := stripANSI(diffInline(styles, tc.old, oldRange, bg, false)); got != tc.old {
				t.Fatalf("old inline render = %q, want exact source %q", got, tc.old)
			}
			if got := stripANSI(diffInline(styles, tc.new, newRange, bg, true)); got != tc.new {
				t.Fatalf("new inline render = %q, want exact source %q", got, tc.new)
			}

			oldLine := stripANSI(diffOldLine(styles, 7, tc.old, oldRange, 200))
			if want := diffNums(7, 0) + "- " + tc.old; oldLine != want {
				t.Fatalf("old diff line = %q, want %q", oldLine, want)
			}
			newLine := stripANSI(diffNewLine(styles, 9, tc.new, newRange, 200))
			if want := diffNums(0, 9) + "+ " + tc.new; newLine != want {
				t.Fatalf("new diff line = %q, want %q", newLine, want)
			}
		})
	}
}

func TestDiffOverlayScrolls(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, teaWindowSize(t, 100, 40))
	prop := &proposals.Proposal{
		Path: "big.txt",
		Hunks: []proposals.Hunk{{
			Start:    1,
			OldLines: []string{"old"},
			NewLines: []string{"new", "added"},
		}},
	}
	ov := newDiffOverlay(m.styles, prop, 80)
	if len(ov.lines) == 0 {
		t.Fatal("diff overlay has no lines")
	}
	ov.scrollBy(100)
	if ov.scroll == 0 {
		t.Error("scrollBy should move the window")
	}
	view := ov.View(m.styles, 80)
	if !strings.Contains(stripANSI(view), "esc close") {
		t.Errorf("overlay missing hints: %q", stripANSI(view))
	}
}

func TestConcealedPlaceholderRowMapping(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, teaWindowSize(t, 100, 40))
	msg := &Message{Role: "assistant"}
	msg.Text = "a\n```go\n" + strings.Repeat("x\n", 15) + "```\nb\n```py\n" + strings.Repeat("y\n", 15) + "```\n"
	prepareConceal(msg, msg.Text)
	m.messages = append(m.messages, msg)
	m.renderMessages()
	if len(m.blockRows) != 2 {
		t.Fatalf("block rows = %d, want 2 (map %v)", len(m.blockRows), m.blockRows)
	}
	// Toggling the second placeholder ("0:1") must flip the py block.
	m.toggleConcealedAt("0:1")
	if !m.messages[0].concealed[1].Expanded {
		t.Error("second block should be expanded after toggle")
	}
	if m.messages[0].concealed[0].Expanded {
		t.Error("first block must stay collapsed")
	}
}

func TestThemeForNameAndLightVariant(t *testing.T) {
	if got := ThemeForName("unknown").Color(TokenCharple); hexOf(got) != "#FF6363" {
		t.Errorf("unknown theme should fall back to charmtone, got %v", got)
	}
	light := ThemeForName("charmtone-light")
	if got := hexOf(light.Color(TokenSurface)); got != "#F6F5F2" {
		t.Errorf("light surface = %q", got)
	}
	if got := hexOf(light.Color(TokenOyster)); got != "#1F2328" {
		t.Errorf("light ink = %q", got)
	}
	dark := ThemeForName("catppuccin")
	if hexOf(dark.Color(TokenPanel)) != "#313244" {
		t.Errorf("catppuccin panel = %q", dark.Color(TokenPanel))
	}
	// Contrast helper picks dark ink on light backgrounds.
	if c := hexOf(Charmtone().ContrastOn(lipgloss.Color("#F6F5F2"))); c != "#1B1B1F" {
		t.Errorf("contrast on light bg = %q", c)
	}
	if c := hexOf(Charmtone().ContrastOn(lipgloss.Color("#0E1015"))); c != "#F5F3EF" {
		t.Errorf("contrast on dark bg = %q", c)
	}
}

func TestGlamourZeroMargins(t *testing.T) {
	styles := NewStyles(Charmtone())
	mr, err := newMarkdownRenderer(styles)
	if err != nil {
		t.Fatalf("newMarkdownRenderer: %v", err)
	}
	out := mr.Render("para one\n\npara two\n\n```go\nfunc main() {}\n```\n", 40)
	clean := stripANSI(out)
	// Paragraph lines must not start with indent spaces from glamour
	// margins (previous default: 4+ leading spaces).
	for _, line := range strings.Split(clean, "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "   ") && strings.TrimSpace(line) != "" {
			t.Errorf("indented line %q — margins must be zeroed", line)
		}
	}
	if !strings.Contains(clean, "func main() {}") {
		t.Errorf("code body missing: %q", clean)
	}
}

func TestGlamourWidthContract(t *testing.T) {
	styles := NewStyles(Charmtone())
	mr, _ := newMarkdownRenderer(styles)
	long := strings.Repeat("word ", 200) + "\n```go\n" + strings.Repeat("x", 200) + "\n```\n"
	out := mr.Render(long, 40)
	for i, line := range strings.Split(out, "\n") {
		if w := xansiWidth(line); w > 40 {
			t.Errorf("line %d width %d > 40: %q", i, w, line[:min(len(line), 60)])
		}
	}
}

func TestSanitizeRendered(t *testing.T) {
	// Trailing whitespace and empty tail lines removed.
	got := sanitizeRendered("ab   \ncd\tx\n\n", 40)
	if got != "ab\ncd    x" {
		t.Errorf("got %q", got)
	}
	// Width clamp with open SGR closed at the cut.
	got = sanitizeRendered("\x1b[31m"+"xxxxxxxxxxxxxxxxxxxxxx", 10)
	if xansiWidth(got) > 10 {
		t.Errorf("clamped width %d", xansiWidth(got))
	}
	if !strings.HasSuffix(got, "\x1b[0m") {
		t.Errorf("open sequence not closed: %q", got)
	}
	// No stray escapes left open after stripBrokenANSI.
	for _, l := range strings.Split(sanitizeRendered("ok\x1b[", 40), "\n") {
		if strings.Contains(l, "\x1b") {
			t.Errorf("broken sequence survived: %q", l)
		}
	}
}

func TestGlamourStyleThemeDriven(t *testing.T) {
	styles := NewStyles(Charmtone())
	s := glamourStyle(styles.T)
	if s.Document.Margin != nil || s.Paragraph.Margin != nil || s.CodeBlock.Margin != nil {
		t.Error("margins must be nil (zeroed)")
	}
	if s.CodeBlock.BackgroundColor == nil || *s.CodeBlock.BackgroundColor != hexOf(styles.T.Color(TokenPanel)) {
		t.Errorf("code block bg not theme-driven: %v", s.CodeBlock.BackgroundColor)
	}
	if s.CodeBlock.Chroma == nil || s.CodeBlock.Chroma.Keyword.Color == nil {
		t.Error("chroma keywords not themed")
	}
}

func TestStreamingRendersGlamourLive(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, teaWindowSize(t, 120, 30))
	// opencode-style: the accumulated text renders through glamour on every
	// delta — literal markers never appear, even mid-stream.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "**bold** and `code`\n\n```go\n"})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "x := 1\n```"})})
	last := m.lastAssistant()
	if last == nil || !last.busy {
		t.Fatalf("last = %+v", last)
	}
	view := stripANSI(m.renderRoleMessage(last, 60))
	if strings.Contains(view, "**bold**") {
		t.Errorf("live rendering must not show raw markers: %q", view)
	}
	if strings.Contains(view, "```") {
		t.Errorf("live rendering must not show fence markers: %q", view)
	}
	if !strings.Contains(view, "bold") {
		t.Errorf("bold text lost: %q", view)
	}
	if !strings.Contains(view, "x := 1") {
		t.Errorf("code body lost: %q", view)
	}
}

func xansiWidth(s string) int {
	return lipgloss.Width(s)
}
