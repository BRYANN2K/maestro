package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	xansi "github.com/charmbracelet/x/ansi"
)

// markdownRenderer renders assistant messages as markdown with code
// highlighting (glamour + chroma, B11 §11.4).
type markdownRenderer struct {
	renderer *glamour.TermRenderer
	width    int
	styles   Styles
}

// newMarkdownRenderer builds a glamour renderer with Charmtone-ish colors.
func newMarkdownRenderer(styles Styles) (*markdownRenderer, error) {
	r, err := glamour.NewTermRenderer(
		glamour.WithStyles(glamourStyle(styles.T)),
		glamour.WithWordWrap(100),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return nil, err
	}
	return &markdownRenderer{renderer: r, styles: styles}, nil
}

// Render renders markdown text wrapped at the given width (the container's
// exact content width) and sanitized for the viewport.
func (mr *markdownRenderer) Render(text string, width int) string {
	if width != mr.width {
		mr.renderer, _ = glamour.NewTermRenderer(
			glamour.WithStyles(glamourStyle(mr.styles.T)),
			glamour.WithWordWrap(max(width, 20)),
			glamour.WithPreservedNewLines(),
		)
		mr.width = width
	}
	out, err := mr.renderer.Render(text)
	if err != nil {
		return lipgloss.NewStyle().Foreground(mr.styles.T.Color(TokenOyster)).Render(text)
	}
	return sanitizeRendered(out, width)
}

// glamourStyle builds a glamour style config entirely from the theme's
// adaptive tokens, with every block margin and indent zeroed. glamour's
// defaults (document 2, paragraph 2, code_block 2) make wrapped prose start
// at ragged columns and shrink code blocks by 8 cells inside a fixed-width
// frame (BlockStack.Width = WordWrap − Margin×2); zeroing them makes the
// markdown flush with the message container (glow + gh-dash practice).
func glamourStyle(theme Theme) ansi.StyleConfig {
	s := styles.DarkStyleConfig
	zero := func(b *ansi.StyleBlock) {
		b.Indent = nil
		b.IndentToken = nil
		b.Margin = nil
	}
	zero(&s.Document)
	zero(&s.BlockQuote)
	zero(&s.Paragraph)
	zero(&s.List.StyleBlock)
	zero(&s.Heading)
	zero(&s.H1)
	zero(&s.H2)
	zero(&s.H3)
	zero(&s.H4)
	zero(&s.H5)
	zero(&s.H6)
	zero(&s.Code)
	zero(&s.CodeBlock.StyleBlock)
	zero(&s.DefinitionList)

	s.Document.Color = stringPtr(theme.Hex(TokenOyster))
	s.Document.BackgroundColor = nil
	s.BlockQuote.Color = stringPtr(theme.Hex(TokenSmoke))
	s.BlockQuote.BackgroundColor = nil
	s.Paragraph.Color = stringPtr(theme.Hex(TokenOyster))
	s.Paragraph.BackgroundColor = nil
	s.List.Color = stringPtr(theme.Hex(TokenOyster))
	s.List.BackgroundColor = nil
	s.Heading.Color = stringPtr(theme.Hex(TokenCharple))
	s.Heading.BackgroundColor = nil
	s.H1.Color = stringPtr(theme.Hex(TokenCharple))
	// glamour's stock H1 is a hard-coded ANSI purple pill. Keep headings
	// terminal-native and let the active palette own both its ink and depth.
	s.H1.BackgroundColor = nil
	s.H1.Prefix = "# "
	s.H1.Suffix = ""
	s.H2.Color = stringPtr(theme.Hex(TokenDolly))
	s.H3.Color = stringPtr(theme.Hex(TokenMalibu))
	s.H4.Color = stringPtr(theme.Hex(TokenSquid))
	s.H5.Color = stringPtr(theme.Hex(TokenSmoke))
	s.H6.Color = stringPtr(theme.Hex(TokenSmoke))
	s.Text.Color = stringPtr(theme.Hex(TokenOyster))
	s.Strikethrough.Color = stringPtr(theme.Hex(TokenSmoke))
	s.Emph.Color = stringPtr(theme.Hex(TokenOyster))
	s.Strong.Color = stringPtr(theme.Hex(TokenOyster))
	s.HorizontalRule.Color = stringPtr(theme.Hex(TokenIron))
	s.Item.Color = stringPtr(theme.Hex(TokenOyster))
	s.Enumeration.Color = stringPtr(theme.Hex(TokenOyster))
	s.Task.Color = stringPtr(theme.Hex(TokenOyster))
	s.Link.Color = stringPtr(theme.Hex(TokenMalibu))
	s.LinkText.Color = stringPtr(theme.Hex(TokenMalibu))
	s.Image.Color = stringPtr(theme.Hex(TokenBok))
	s.ImageText.Color = stringPtr(theme.Hex(TokenSmoke))
	s.Code.Color = stringPtr(theme.Hex(TokenJulep))
	s.Code.BackgroundColor = stringPtr(theme.Hex(TokenPanel))
	s.CodeBlock.Color = stringPtr(theme.Hex(TokenOyster))
	s.CodeBlock.BackgroundColor = stringPtr(theme.Hex(TokenPanel))
	s.Table.Color = stringPtr(theme.Hex(TokenOyster))
	s.Table.BackgroundColor = nil
	s.DefinitionList.Color = stringPtr(theme.Hex(TokenOyster))
	s.DefinitionTerm.Color = stringPtr(theme.Hex(TokenDolly))
	s.DefinitionDescription.Color = stringPtr(theme.Hex(TokenOyster))
	s.HTMLBlock.Color = stringPtr(theme.Hex(TokenSmoke))
	s.HTMLSpan.Color = stringPtr(theme.Hex(TokenSmoke))

	// Replace the complete stock Chroma map. Partial mutation leaves dozens of
	// hard-coded colors behind (and the stock background uses BackgroundColor,
	// not Color), which is why code fences previously stayed purple/gray while
	// the surrounding theme changed.
	fg := func(tok Token) ansi.StylePrimitive {
		return ansi.StylePrimitive{Color: stringPtr(theme.Hex(tok))}
	}
	bg := func(tok Token) ansi.StylePrimitive {
		return ansi.StylePrimitive{BackgroundColor: stringPtr(theme.Hex(tok))}
	}
	s.CodeBlock.Chroma = &ansi.Chroma{
		Text:                fg(TokenOyster),
		Error:               ansi.StylePrimitive{Color: stringPtr(theme.Hex(TokenChar)), BackgroundColor: stringPtr(theme.Hex(TokenSash))},
		Comment:             fg(TokenSmoke),
		CommentPreproc:      fg(TokenCoral),
		Keyword:             fg(TokenCharple),
		KeywordReserved:     fg(TokenSquid),
		KeywordNamespace:    fg(TokenBok),
		KeywordType:         fg(TokenMustard),
		Operator:            fg(TokenDolly),
		Punctuation:         fg(TokenSmoke),
		Name:                fg(TokenOyster),
		NameBuiltin:         fg(TokenMalibu),
		NameTag:             fg(TokenMustard),
		NameAttribute:       fg(TokenMalibu),
		NameClass:           fg(TokenDolly),
		NameConstant:        fg(TokenMustard),
		NameDecorator:       fg(TokenCoral),
		NameException:       fg(TokenSash),
		NameFunction:        fg(TokenMalibu),
		NameOther:           fg(TokenOyster),
		Literal:             fg(TokenOyster),
		LiteralNumber:       fg(TokenTang),
		LiteralDate:         fg(TokenCoral),
		LiteralString:       fg(TokenCitron),
		LiteralStringEscape: fg(TokenJulep),
		GenericDeleted:      fg(TokenSash),
		GenericEmph:         fg(TokenOyster),
		GenericInserted:     fg(TokenJulep),
		GenericStrong:       fg(TokenOyster),
		GenericSubheading:   fg(TokenSmoke),
		Background:          bg(TokenPanel),
	}
	return s
}

// sanitizeRendered cleans a rendered block before it enters the viewport
// (mods + pug pipeline): trailing whitespace per line (glamour margin
// padding), tabs expanded to 4 spaces so code columns are deterministic,
// ANSI-aware width clamp so no line can outgrow the container, and broken
// or open escape sequences closed so styles never bleed into the next line.
func sanitizeRendered(out string, width int) string {
	lines := strings.Split(out, "\n")
	for i := range lines {
		l := lines[i]
		l = strings.TrimRight(l, " \t")
		if strings.Contains(l, "\t") {
			l = strings.ReplaceAll(l, "\t", "    ")
		}
		l = stripBrokenANSI(xansi.Truncate(l, max(width, 1), ""))
		if strings.Contains(l, "\x1b[") && !strings.HasSuffix(l, "\x1b[0m") {
			l += "\x1b[0m"
		}
		lines[i] = l
	}
	// glamour's Document.BlockPrefix adds a leading blank line and every
	// block adds suffixes; drop the edge newlines so the body starts flush
	// under the role header.
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}

func stringPtr(s string) *string { return &s }

// concealedBlock is a long code fence collapsed to a placeholder line
// (opencode-style conceal). Expanded blocks render in full.
type concealedBlock struct {
	Lang      string
	Body      string
	LineCount int
	Expanded  bool
}

// concealLimit is the minimum body line count above which a fence is
// collapsed into a placeholder.
const concealLimit = 12

// prepareConceal rewrites markdown source so that fenced code blocks longer
// than concealLimit lines are collapsed into a single placeholder line
// inside the fence. The original bodies are retained in msg.concealed for
// expansion. The block list is rebuilt only when the source changes, so
// expansion state survives width re-renders. Streaming messages pass
// through untouched.
func prepareConceal(msg *Message, text string) string {
	if msg.busy || (!strings.Contains(text, "```") && !strings.Contains(text, "~~~")) {
		return text
	}
	if msg.concealSrc != text {
		msg.concealSrc = text
		msg.concealed = concealBlocks(text, msg.concealed)
	}
	if len(msg.concealed) == 0 {
		return text
	}
	return spliceConcealed(text, msg.concealed)
}

// concealBlocks parses fenced code blocks and returns the rebuilt block
// list, preserving expansion state by position.
func concealBlocks(text string, prev []concealedBlock) []concealedBlock {
	lines := strings.Split(text, "\n")
	var out []concealedBlock
	fenceMarker := byte(0)
	fenceWidth := 0
	lang := ""
	var body []string
	prevIdx := 0
	for _, line := range lines {
		marker, width, rest, delimiter := markdownFenceRun(line)
		if fenceMarker == 0 {
			if delimiter && (marker != '`' || !strings.Contains(rest, "`")) {
				fenceMarker, fenceWidth = marker, width
				lang = strings.TrimSpace(rest)
				body = nil
			}
			continue
		}
		if delimiter && marker == fenceMarker && width >= fenceWidth && strings.TrimSpace(rest) == "" {
			fenceMarker, fenceWidth = 0, 0
			block := concealedBlock{Lang: lang, Body: strings.Join(body, "\n"), LineCount: len(body)}
			if prevIdx < len(prev) && prev[prevIdx].Lang == lang {
				block.Expanded = prev[prevIdx].Expanded
			}
			prevIdx++
			out = append(out, block)
			continue
		}
		body = append(body, line)
	}
	return out
}

// spliceConcealed re-emits the source with collapsed blocks replaced by
// placeholder lines inside their fences.
func spliceConcealed(text string, blocks []concealedBlock) string {
	lines := strings.Split(text, "\n")
	var out []string
	fenceMarker := byte(0)
	fenceWidth := 0
	bIdx := 0
	emitted := 0
	for _, line := range lines {
		marker, width, rest, delimiter := markdownFenceRun(line)
		if fenceMarker == 0 {
			if delimiter && (marker != '`' || !strings.Contains(rest, "`")) {
				fenceMarker, fenceWidth = marker, width
				emitted = 0
			}
			out = append(out, line)
			continue
		}
		if delimiter && marker == fenceMarker && width >= fenceWidth && strings.TrimSpace(rest) == "" {
			fenceMarker, fenceWidth = 0, 0
			bIdx++
			out = append(out, line)
			continue
		}
		if bIdx < len(blocks) && blocks[bIdx].LineCount > concealLimit {
			if !blocks[bIdx].Expanded {
				if emitted == 0 {
					out = append(out, concealedPlaceholder(blocks[bIdx]))
					emitted = 1
				}
			} else {
				out = append(out, line) // expanded: emit the body verbatim
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// concealedPlaceholder is the visible marker line for a collapsed fence.
// The "[code" prefix is the region-mapping anchor (see renderMessages).
func concealedPlaceholder(b concealedBlock) string {
	lang := b.Lang
	if lang == "" {
		lang = "text"
	}
	return fmt.Sprintf("[code · %s · %d lines · click or v to view]", lang, b.LineCount)
}

// toggleConcealedBlock expands or collapses one concealed block of a
// message and invalidates its render cache.
func (m *Model) toggleConcealedBlock(msg *Message, idx int) {
	if msg == nil || idx < 0 || idx >= len(msg.concealed) {
		return
	}
	msg.concealed[idx].Expanded = !msg.concealed[idx].Expanded
	msg.cachedValid = false
	m.renderMessages()
}

var urlRe = regexp.MustCompile(`https?://[^\s<>"']+`)

// wrapLinks wraps URLs visible in the rendered output in OSC 8 hyperlinks.
// glamour keeps the URL text visible verbatim, so a post-pass on the ANSI
// string is exact, and x/ansi treats OSC 8 as a zero-width sequence so
// downstream clamping never desynchronizes the terminal.
func wrapLinks(out, src string) string {
	if !strings.Contains(src, "http") {
		return out
	}
	seen := map[string]bool{}
	var urls []string
	for _, m := range urlRe.FindAllString(src, -1) {
		u := strings.TrimRight(m, ".,;:!?)]")
		if !seen[u] {
			seen[u] = true
			urls = append(urls, u)
		}
	}
	for _, u := range urls {
		if !strings.Contains(out, u) {
			continue
		}
		out = strings.ReplaceAll(out, u, xansi.SetHyperlink(u)+u+xansi.ResetHyperlink())
	}
	return out
}

// renderThinking draws the collapsible reasoning summary line for the
// current turn: a pulsing "◉ dev · thinking 12s" while the sub-agent runs,
// collapsing to "✓ dev · worked 1m 02s · 5 tools" when done. Clicking or
// pressing "t" expands the detail and advisor note.
func renderThinking(styles Styles, pulse int, th *thinkingState, tools, width int) string {
	var icon, line string
	var color color.Color
	role := safeIDEPlainText(th.Role)
	elapsed := th.Done.Sub(th.Started)
	if elapsed <= 0 {
		elapsed = time.Since(th.Started)
	}
	dur := formatDuration(elapsed)
	switch th.Status {
	case "cancelled":
		icon, color = "■", styles.T.Color(TokenSmoke)
		if th.Reasoning {
			line = fmt.Sprintf("%s Thinking · cancelled after %s", icon, dur)
		} else {
			line = fmt.Sprintf("%s %s · cancelled after %s", icon, role, dur)
		}
	case "error":
		icon, color = "✗", styles.T.Color(TokenSash)
		if th.Reasoning {
			line = fmt.Sprintf("%s Thinking · failed after %s", icon, dur)
		} else {
			line = fmt.Sprintf("%s %s · failed after %s", icon, role, dur)
		}
	case "done":
		icon, color = "✓", styles.T.Color(TokenJulep)
		suffix := ""
		if tools > 0 {
			suffix = fmt.Sprintf(" · %d tool(s)", tools)
		}
		if th.Reasoning {
			line = fmt.Sprintf("%s Thinking · %s%s", icon, dur, suffix)
		} else {
			line = fmt.Sprintf("%s %s · worked %s%s", icon, role, dur, suffix)
		}
	default: // running
		icon, color = "◉", styles.T.Color(TokenCharple)
		if pulse%2 == 1 {
			icon = "◈"
		}
		if th.Reasoning {
			line = fmt.Sprintf("%s Thinking · %s", icon, dur)
		} else {
			line = fmt.Sprintf("%s %s · thinking %s", icon, role, dur)
		}
	}
	if th.Expanded {
		var b strings.Builder
		b.WriteString(lipgloss.NewStyle().Foreground(color).Render("▾ " + line))
		if th.Detail != "" {
			detail := terminalSafeMarkdownText(strings.TrimSpace(th.Detail))
			detail = xansi.Wordwrap(strings.ReplaceAll(detail, "\t", "    "), max(width-4, 20), "")
			b.WriteString("\n" + styles.MessageMuted.Render(clampANSIWidth(detail, max(width-4, 20))))
		}
		if th.Note != "" {
			b.WriteString("\n" + styles.MessageSystem.Render("advisor: "+terminalSafeMarkdownText(th.Note)))
		}
		b.WriteString("\n" + styles.Hint.Render("[t] collapse"))
		return b.String()
	}
	if th.Status == "running" {
		return lipgloss.NewStyle().Foreground(color).Render("▸ " + line + "  " + styles.Hint.Render("[t]"))
	}
	return lipgloss.NewStyle().Foreground(color).Render("▸ " + line + "  " + styles.Hint.Render("[t]"))
}

// formatDuration renders a duration compactly ("12s", "3m05s", "1h02m").
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	secs := int(d.Round(time.Second) / time.Second)
	if secs < 60 {
		return fmt.Sprintf("%ds", secs)
	}
	mins := secs / 60
	secs %= 60
	if mins < 60 {
		return fmt.Sprintf("%dm%02ds", mins, secs)
	}
	hours := mins / 60
	mins %= 60
	return fmt.Sprintf("%dh%02dm", hours, mins)
}

// renderRoleMessage renders a message in its role's container with a
// timestamped label. Finished messages are served from the per-message
// render cache (invalidated on width change or when cards/selection mutate
// the message) so streaming never re-renders the whole transcript.
func (m *Model) renderRoleMessage(msg *Message, width int) string {
	if !msg.busy && !m.chatSelecting && m.selectionMenu == nil && m.selectionAsk == nil &&
		msg.cachedValid && msg.cachedWidth == width {
		return msg.cachedRendered
	}
	rendered := m.renderRoleMessageRaw(msg, width)
	if !msg.busy && !m.chatSelecting {
		msg.cachedRendered = rendered
		msg.cachedWidth = width
		msg.cachedValid = true
	}
	return rendered
}

// finishFooter returns the muted "model · duration" line for a finished
// assistant turn, or "" when there is nothing to report (S6).
func (m *Model) finishFooter(msg *Message) string {
	if msg.busy || msg.FinishedAt.IsZero() || msg.Model == "" {
		return ""
	}
	dur := msg.FinishedAt.Sub(msg.StartedAt)
	if dur < time.Second {
		return ""
	}
	return lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render(fmt.Sprintf("%s · %s", safeIDEPlainText(msg.Model), formatDuration(dur)))
}

// invalidateMessageCaches clears every message's render cache after a
// mutation that affects already-rendered output (card status changes,
// selection edits, theme switches).
func (m *Model) invalidateMessageCaches() {
	for _, msg := range m.messages {
		msg.cachedValid = false
	}
}

// renderRoleMessageRaw renders a message from scratch.
func (m *Model) renderRoleMessageRaw(msg *Message, width int) string {
	ts := msg.ts.Format("15:04")
	if msg.ts.IsZero() {
		ts = time.Now().Format("15:04")
	}
	state := msg.State
	if state == "" {
		state = "chat"
	}
	accent := m.styles.T.Color(stateToken(state))
	roleColor := m.styles.T.Color(TokenSmoke)
	if msg.Role == "assistant" {
		roleColor = m.styles.T.Color(TokenCharple)
	}
	role := lipgloss.NewStyle().Foreground(roleColor).Bold(true).Render(roleLabel(msg.Role))
	if msg.Role == "assistant" && msg.busy {
		activity := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly)).Render("◌ " + m.activity())
		role += "  " + activity
	}
	header := role + "  " + lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render(ts)
	if state != "chat" {
		header += "  " + lipgloss.NewStyle().Foreground(accent).Render(strings.ToUpper(state))
	}
	container := lipgloss.NewStyle().
		PaddingLeft(2).
		Width(max(width-2, 1)).
		MaxWidth(max(width-2, 1))
	switch msg.Role {
	case "user":
		body := m.styles.MessageUser.Render(terminalSafeMarkdownText(msg.Text))
		if m.activeChatSelection(msg) != nil {
			body = m.renderSelectableChatText(msg, lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenOyster)))
		}
		return container.Render(header + "\n" + body)
	case "assistant":
		var body string
		if m.activeChatSelection(msg) != nil {
			// Keep the selected range mapped to source lines. Markdown
			// rendering reflows content, so the selected chat view uses the
			// same readable text with a precise cell highlight.
			body = m.renderSelectableChatText(msg, lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenOyster)))
		} else if m.md != nil && msg.Text != "" {
			// opencode-style: the accumulated text renders through glamour on
			// every delta — no raw-markdown phase, so the transcript never shows
			// literal markers.
			src := prepareConceal(msg, focusOutputMarkdown(terminalSafeMarkdownText(msg.Text)))
			body = wrapLinks(m.md.Render(src, width-2), src)
		} else {
			body = m.styles.MessageAssistant.Width(width - 2).Render(terminalSafeMarkdownText(msg.Text))
		}
		if msg.think != nil && msg.think.Role != "" {
			body = renderThinking(m.styles, m.pulse, msg.think, msg.toolCount, width-2) + "\n" + body
		}
		if footer := m.finishFooter(msg); footer != "" {
			body = body + "\n" + footer
		}
		return container.Render(header + "\n" + body)
	default: // system
		body := m.styles.MessageSystem.Render(terminalSafeMarkdownText(msg.Text))
		if m.activeChatSelection(msg) != nil {
			body = m.renderSelectableChatText(msg, lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Italic(true))
		}
		return container.Render(header + "\n" + body)
	}
}

func (m *Model) renderSelectableChatText(msg *Message, base lipgloss.Style) string {
	safeText := terminalSafeMarkdownText(msg.Text)
	selection := m.activeChatSelection(msg)
	if selection == nil {
		return base.Render(safeText)
	}
	lines := strings.Split(safeText, "\n")
	var rendered []string
	for lineNo, line := range lines {
		runes := []rune(line)
		var b strings.Builder
		start := 0
		flush := func(end int, selected bool) {
			if end <= start {
				return
			}
			style := base
			if selected {
				style = lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenPepper)).Background(m.styles.T.Color(TokenDolly)).Bold(true)
			}
			b.WriteString(style.Render(string(runes[start:end])))
		}
		for i := 0; i < len(runes); i++ {
			selected := chatCellSelected(selection, lineNo, i)
			if i == start {
				continue
			}
			previous := chatCellSelected(selection, lineNo, i-1)
			if selected != previous {
				flush(i, previous)
				start = i
			}
		}
		flush(len(runes), chatCellSelected(selection, lineNo, len(runes)-1))
		rendered = append(rendered, b.String())
	}
	return strings.Join(rendered, "\n")
}

func stateForPhase(phase string) string {
	switch strings.ToLower(phase) {
	case "propose", "spec", "accept":
		return "spec"
	case "build", "review", "fix", "docs", "archive", "edit":
		return "build"
	default:
		return "chat"
	}
}

func stateToken(state string) Token {
	switch strings.ToLower(state) {
	case "spec":
		return TokenJulep
	case "build", "review", "edit", "docs":
		return TokenMalibu
	case "error":
		return TokenSash
	default:
		return TokenCharple
	}
}

func roleLabel(role string) string {
	switch role {
	case "user":
		return "You"
	case "assistant":
		return "Maestro"
	default:
		return "System"
	}
}
