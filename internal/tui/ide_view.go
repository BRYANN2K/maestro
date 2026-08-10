package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderIDE draws the approved code-first layout: a narrow explorer, a
// dominant editor and a compact Agent/HITL companion rail.
func (m *Model) renderIDE() string {
	ide := m.ide
	if ide == nil {
		return ""
	}
	editorW, treeW, railW := m.idePaneWidths()
	bodyH := m.bodyHeight()

	if b := ide.Ed.Buffer(); b != nil && ide.UI.Gutter != nil && ide.UI.Gutter.Path != b.Path {
		ide.UI.Gutter.Refresh(m.ctx(), b.Path)
	}

	// The mockup is a workspace grid, not three cards. Two quiet vertical
	// dividers preserve most of the terminal for code and avoid the heavy
	// nested-box look of the previous implementation.
	editorInnerW := max(editorW-1, 10)
	ide.UI.Width = editorInnerW
	chatOpen := ide.Focus == ideChat || m.input.Value() != "" || ide.Ed.HasSelection()
	ide.UI.Height = max(bodyH-2, 3)
	if chatOpen {
		ide.UI.Height = max(bodyH-m.inputH-6, 3)
	}
	editorPane := ide.UI.View()
	if ide.proposalPreview != nil {
		editorPane = m.renderIDEProposalPreview(ide, editorInnerW)
	} else if ide.preview {
		editorPane = m.renderIDEPreview(ide, editorInnerW)
	}

	var center strings.Builder
	center.WriteString(m.renderBufferTabs(ide, editorInnerW) + "\n")
	center.WriteString(m.renderEditorHeader(ide, editorInnerW) + "\n")
	center.WriteString(editorPane)
	if chatOpen {
		center.WriteString("\n" + m.renderIDEChat(ide, editorInnerW))
	}

	treeInnerW := max(treeW-1, 8)
	treeBody := m.renderTree(treeInnerW, max(bodyH-2, 1))
	treeMeta := "  workspace"
	if ide.explorerView == ideExplorerChanges {
		treeBody = m.renderChangedTree(treeInnerW, max(bodyH-2, 1))
		treeMeta = fmt.Sprintf("  %d changed", len(m.sidebar.modFiles))
	}
	treeContent := m.renderExplorerTabs(treeInnerW) + "\n" +
		lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render(treeMeta) + "\n" + treeBody
	tree := m.renderIDEPane(treeContent, treeW, bodyH, true, TokenPanel, ide.Focus == ideTree)
	editor := m.renderIDEPane(center.String(), editorW, bodyH, true, TokenSurface, ide.Focus == ideEditor || ide.Focus == ideChat)
	panes := []string{tree, editor}
	if railW > 0 {
		rail := m.renderIDEPane(m.renderIDECompanion(max(railW, 10), bodyH), railW, bodyH, false, TokenPanel, ide.Focus == ideHITL)
		panes = append(panes, rail)
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top, panes...)
	m.registerIDERegions(editorW, treeW, railW)

	// Theme browser overlay.
	if ide.themePicker != nil {
		box := m.styles.Dialog.Render(ide.themePicker.View(m.styles, 40))
		return lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, box)
	}
	if m.overlay != overlayNone {
		return m.renderOverlay(body)
	}
	// Overlays (picker, AgentReview, git workspace).
	if overlay := ide.UI.RenderOverlay(); overlay != "" {
		box := m.styles.Dialog.Render(overlay)
		return lipgloss.Place(m.width, m.bodyHeight(), lipgloss.Center, lipgloss.Center, box)
	}
	if m.selectionEdit != nil {
		box := m.styles.Dialog.Render(m.renderSelectionEdit())
		x, y := m.selectionOverlayPosition(box, m.bodyHeight())
		return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
	}
	if m.selectionAsk != nil {
		box := m.styles.Dialog.Render(m.renderSelectionAsk())
		x, y := m.selectionOverlayPosition(box, m.bodyHeight())
		return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
	}
	if m.selectionMenu != nil {
		box := m.styles.Dialog.Render(m.renderSelectionMenu())
		x, y := m.selectionOverlayPosition(box, m.bodyHeight())
		return overlayAt(body, m.width, m.bodyHeight(), x, y, box)
	}
	return body
}

func (m *Model) renderIDEPane(content string, width, height int, divider bool, surface Token, focused bool) string {
	innerW := max(width, 1)
	if divider {
		innerW = max(width-1, 1)
	}
	content = clampANSIHeight(clampANSIWidth(content, innerW), max(height, 1))
	style := lipgloss.NewStyle().
		Width(innerW).MaxWidth(innerW).
		Height(max(height, 1)).MaxHeight(max(height, 1)).
		Background(m.styles.T.Color(surface))
	if divider {
		border := m.styles.T.Color(TokenIron)
		borderShape := lipgloss.NormalBorder()
		if focused {
			border = m.styles.T.Color(TokenCharple)
			borderShape.Right = "┃"
		}
		style = style.Border(borderShape, false, true, false, false).
			BorderForeground(border)
	}
	return style.Render(content)
}

func (m *Model) ideSectionTitle(title string, focused bool) string {
	color := m.styles.T.Color(TokenOyster)
	prefix := "▎ "
	if focused {
		color = m.styles.T.Color(TokenDolly)
		prefix = "▌ "
	}
	return lipgloss.NewStyle().Foreground(color).Bold(true).Render(prefix + title)
}

func (m *Model) renderExplorerTabs(width int) string {
	files := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render("FILES")
	changes := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render(fmt.Sprintf("CHANGES %d", len(m.sidebar.modFiles)))
	active := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly)).Bold(true)
	if m.ide != nil && m.ide.explorerView == ideExplorerChanges {
		changes = active.Render(fmt.Sprintf("CHANGES %d", len(m.sidebar.modFiles)))
	} else {
		files = active.Render("FILES")
	}
	return clampANSIWidth("  "+files+"  "+changes, max(width, 1))
}

func (m *Model) renderEditorHeader(ide *IDEState, width int) string {
	label := "untitled"
	dirty := ""
	if ide != nil && ide.Ed != nil {
		if b := ide.Ed.Buffer(); b != nil {
			label = b.Path
			if rel, err := filepath.Rel(ide.project, label); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				label = filepath.ToSlash(rel)
			}
			if b.Dirty {
				dirty = " ●"
			}
		}
	}
	if ide != nil && ide.proposalPreview != nil {
		label = compactWorkspacePath(ide.proposalPreview.Path) + "  ›  PROPOSAL DIFF · READ ONLY"
	} else if ide != nil && ide.preview {
		label += "  ›  PREVIEW · MARKDOWN"
	} else if symbol := editorSymbol(ide); symbol != "" {
		label += "  ›  " + symbol
	}
	label = safeIDEPlainText(label)
	text := "  ▧  " + label + dirty
	return lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render(truncateRunes(text, max(width, 1)))
}

// editorSymbol derives a lightweight breadcrumb without introducing an LSP
// dependency. It intentionally recognizes only stable, useful landmarks.
func editorSymbol(ide *IDEState) string {
	if ide == nil || ide.Ed == nil || ide.Ed.Buffer() == nil {
		return ""
	}
	b := ide.Ed.Buffer()
	for i := min(b.Cur.Line, len(b.Lines)-1); i >= 0; i-- {
		line := strings.TrimSpace(b.LineText(i))
		switch {
		case strings.HasPrefix(line, "func "):
			name := strings.TrimSpace(strings.TrimPrefix(line, "func "))
			if strings.HasPrefix(name, "(") {
				if end := strings.Index(name, ")"); end >= 0 {
					name = strings.TrimSpace(name[end+1:])
				}
			}
			if end := strings.Index(name, "("); end > 0 {
				return name[:end]
			}
		case strings.HasPrefix(line, "#"):
			return strings.TrimSpace(strings.TrimLeft(line, "#"))
		case strings.HasPrefix(line, "type "):
			fields := strings.Fields(line)
			if len(fields) > 1 {
				return fields[1]
			}
		}
	}
	return ""
}

// renderIDECompanion keeps task approvals, the latest agent handoff and the
// current proposal visible without turning the IDE into a three-column chat
// dashboard. Content is intentionally dense and width-budgeted.
func (m *Model) renderIDECompanion(width, height int) string {
	if height < 15 {
		return m.renderIDECompanionCompact(width, height)
	}
	tasksH := max(height*27/100, 5)
	maestroH := max(height*28/100, 5)
	proposalH := height - tasksH - maestroH
	if proposalH < 5 {
		proposalH = 5
		maestroH = max(height-tasksH-proposalH, 3)
	}

	var tasks strings.Builder
	railFocused := m.ide != nil && m.ide.Focus == ideHITL
	total := len(m.sidebar.hitl)
	done := 0
	for _, it := range m.sidebar.hitl {
		if m.sidebar.checked[it.ID] {
			done++
		}
	}
	header := fmt.Sprintf("TASKS  %d/%d", done, total)
	if total == 0 {
		tasks.WriteString(m.styles.Hint.Render(wrapPlain("✓ No approvals required", max(width-4, 1))))
	} else {
		for i, it := range m.sidebar.hitl {
			mark := "[ ]"
			focusMark := "  "
			style := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke))
			if m.sidebar.checked[it.ID] {
				mark = "[x]"
				style = lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke))
			} else if i == m.sidebar.selected {
				style = lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenOyster)).Bold(true)
				if railFocused {
					focusMark = "> "
				}
			}
			tasks.WriteString(style.Render(wrapPlain(focusMark+mark+" "+it.Item, max(width-4, 1))) + "\n")
		}
	}

	message := "Select code and press Space A to ask Maestro."
	if last := m.lastAssistant(); last != nil && strings.TrimSpace(last.Text) != "" {
		budget := max((maestroH-3)*max(width-4, 10), 80)
		message = truncateRunes(boundedTextTail(last.Text, min(budget*4, 4096)), budget)
	}
	if offer := m.visibleCoachOffer(); offer != nil {
		coachLine := "∴ Coach · " + offer.Title + " · /learn next"
		message = truncateRunes(coachLine, max(width-4, 1)) + "\n\n" + message
	}
	maestro := m.styles.MessageAssistant.Render(wrapPlain(message, max(width-4, 1)))

	var proposal strings.Builder
	reviewTitle := "REVIEW"
	if len(m.pending) == 0 {
		proposal.WriteString(m.styles.Hint.Render("No changes to review"))
	} else {
		card := m.pendingDecisionCard()
		if card == nil {
			card = m.pending[len(m.pending)-1]
		}
		queueIndex := 0
		for i, pending := range m.pending {
			if pending == card {
				queueIndex = i
				break
			}
		}
		reviewTitle = fmt.Sprintf("REVIEW  %d/%d", queueIndex+1, len(m.pending))
		path := card.ProposalPath
		if path == "" && card.Proposal != nil {
			path = card.Proposal.Path
		}
		proposal.WriteString(m.styles.MessageMuted.Render(truncateIDEPlainText(filepath.Base(path), max(width-4, 1))) + "\n")
		if card.Proposal != nil {
			adds, removes := 0, 0
			for _, h := range card.Proposal.Hunks {
				adds += len(h.NewLines)
				removes += len(h.OldLines)
			}
			proposal.WriteString(lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenJulep)).Render(fmt.Sprintf("+%d", adds)))
			proposal.WriteString("  ")
			proposal.WriteString(lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSash)).Render(fmt.Sprintf("-%d", removes)) + "\n\n")
			proposal.WriteString(m.renderHunkSummary(card, max(width-4, 1), max(proposalH-10, 1)))
		}
		proposal.WriteString("\n" + m.renderIDEReviewNavigation(max(width-4, 1), card))
		proposal.WriteString("\n" + m.renderIDEHunkButtons(max(width-4, 1), card))
		proposal.WriteString("\n" + m.renderIDEReviewButtons(max(width-4, 1)))
	}

	return m.renderRailSection(header, tasks.String(), width, tasksH, true, railFocused) + "\n" +
		m.renderRailSection("AGENT · MAESTRO", maestro, width, maestroH, true, false) + "\n" +
		m.renderRailSectionPinnedN(reviewTitle, proposal.String(), width, proposalH, 3, false)
}

func (m *Model) renderIDEReviewNavigation(width int, card *Card) string {
	if card == nil || card.Proposal == nil {
		return ""
	}
	hunks := len(card.Proposal.Hunks)
	selected := 0
	if m.ide != nil && hunks > 0 {
		selected = clamp(m.ide.proposalHunk, 0, hunks-1)
	}
	queueIndex := 0
	for i, pending := range m.pending {
		if pending == card {
			queueIndex = i
			break
		}
	}
	plain := ""
	if len(m.pending) > 1 {
		plain = fmt.Sprintf("‹ proposal %d/%d ›  ·  ", queueIndex+1, len(m.pending))
	}
	if proposalRequiresAtomicDecision(card.Proposal) {
		plain += "atomic contract"
		return clampANSIWidth(lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly)).Bold(true).Render(plain), width)
	}
	plain += fmt.Sprintf("‹ hunk %d/%d ›", selected+1, max(hunks, 1))
	return clampANSIWidth(lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly)).Render(plain), width)
}

func (m *Model) renderIDEHunkButtons(width int, card *Card) string {
	if card == nil || card.Proposal == nil || len(card.Proposal.Hunks) == 0 {
		return strings.Repeat(" ", max(width, 0))
	}
	if proposalRequiresAtomicDecision(card.Proposal) {
		return lipgloss.NewStyle().
			Foreground(m.styles.T.Color(TokenDolly)).
			Align(lipgloss.Center).
			Width(max(width, 1)).
			Render("atomic · whole contract only")
	}
	return m.renderReviewButtonPair(width, "✓ hunk", "× hunk", false)
}

func (m *Model) renderIDEReviewButtons(width int) string {
	return m.renderReviewButtonPair(width, "✓ Accept all", "× Decline all", true)
}

func (m *Model) renderReviewButtonPair(width int, acceptLabel, declineLabel string, strong bool) string {
	width = max(width, 3)
	gap := 1
	leftW := max((width-gap)/2, 1)
	rightW := max(width-gap-leftW, 1)
	acceptStyle := lipgloss.NewStyle().
		Foreground(m.styles.T.Color(TokenJulep)).
		Background(m.styles.T.Blend(TokenPanel, TokenJulep, 0.18)).
		Bold(strong).Align(lipgloss.Center).Width(leftW)
	declineStyle := lipgloss.NewStyle().
		Foreground(m.styles.T.Color(TokenSash)).
		Background(m.styles.T.Blend(TokenPanel, TokenSash, 0.15)).
		Bold(strong).Align(lipgloss.Center).Width(rightW)
	accept := acceptStyle.Render(acceptLabel)
	decline := declineStyle.Render(declineLabel)
	return accept + strings.Repeat(" ", gap) + decline
}

func (m *Model) renderIDEProposalPreview(ide *IDEState, width int) string {
	if ide == nil || ide.proposalPreview == nil {
		return ""
	}
	content := diffView(m.styles, ide.proposalPreview, max(width, 20), ide.proposalHunk)
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	height := max(ide.UI.Height, 3)
	maxScroll := max(len(lines)-height, 0)
	ide.proposalScroll = clamp(ide.proposalScroll, 0, maxScroll)
	out := make([]string, 0, height)
	for i := ide.proposalScroll; i < len(lines) && len(out) < height; i++ {
		out = append(out, clampANSIWidth(lines[i], max(width, 1)))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// boundedTextTail caps work performed by the IDE companion while an agent is
// streaming a large response. It slices on a UTF-8 boundary before any
// wrapping or rune conversion takes place.
func boundedTextTail(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && text[start]&0xC0 == 0x80 {
		start++
	}
	return "…" + strings.TrimSpace(text[start:])
}

func (m *Model) renderIDECompanionCompact(width, height int) string {
	total := len(m.sidebar.hitl)
	done := 0
	for _, it := range m.sidebar.hitl {
		if m.sidebar.checked[it.ID] {
			done++
		}
	}
	body := m.ideSectionTitle(fmt.Sprintf("TASKS  %d/%d", done, total), m.ide != nil && m.ide.Focus == ideHITL) + "\n\n"
	if total == 0 {
		body += "[ ] No actions pending"
	}
	if offer := m.visibleCoachOffer(); offer != nil {
		body += "\n\n∴ Coach · " + truncateRunes(offer.Title, max(width-12, 4))
	}
	return clampANSIHeight(clampANSIWidth(body, width), height)
}

func (m *Model) renderRailSection(title, body string, width, height int, divider, focused bool) string {
	width, height = max(width, 1), max(height, 1)
	innerW := max(width-4, 1)
	lines := []string{m.ideSectionTitle(title, focused)}
	if height > 1 {
		lines = append(lines, "")
	}
	for _, line := range strings.Split(clampANSIWidth(body, innerW), "\n") {
		if len(lines) >= height-btoi(divider) {
			break
		}
		lines = append(lines, "  "+line)
	}
	for len(lines) < height-btoi(divider) {
		lines = append(lines, "")
	}
	if divider {
		lines = append(lines, lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenIron)).Render(strings.Repeat("─", width)))
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func (m *Model) renderRailSectionPinnedN(title, body string, width, height, pinned int, focused bool) string {
	width, height = max(width, 1), max(height, 1)
	innerW := max(width-4, 1)
	bodyLines := strings.Split(clampANSIWidth(body, innerW), "\n")
	pinned = clamp(pinned, 0, len(bodyLines))
	tail := append([]string(nil), bodyLines[len(bodyLines)-pinned:]...)
	bodyLines = bodyLines[:len(bodyLines)-pinned]
	lines := []string{m.ideSectionTitle(title, focused)}
	if height > 1 {
		lines = append(lines, "")
	}
	for _, line := range bodyLines {
		if len(lines) >= height-pinned {
			break
		}
		lines = append(lines, "  "+line)
	}
	for len(lines) < height-pinned {
		lines = append(lines, "")
	}
	for _, line := range tail {
		if len(lines) < height {
			lines = append(lines, "  "+line)
		}
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

func btoi(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (m *Model) renderHunkSummary(card *Card, width, limit int) string {
	if card == nil || card.Proposal == nil || limit <= 0 {
		return ""
	}
	var lines []string
	for _, h := range card.Proposal.Hunks {
		for i, old := range h.OldLines {
			lines = append(lines, lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSash)).Render(fmt.Sprintf("%d - %s", h.Start+i, truncateIDEPlainText(old, max(width-8, 1)))))
			if len(lines) >= limit {
				return strings.Join(lines, "\n")
			}
		}
		for i, line := range h.NewLines {
			lines = append(lines, lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenJulep)).Render(fmt.Sprintf("%d + %s", h.Start+i, truncateIDEPlainText(line, max(width-8, 1)))))
			if len(lines) >= limit {
				return strings.Join(lines, "\n")
			}
		}
	}
	return strings.Join(lines, "\n")
}

func wrapPlain(s string, width int) string {
	if width < 4 {
		return truncateRunes(s, max(width, 1))
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len([]rune(line))+1+len([]rune(word)) <= width {
			line += " " + word
			continue
		}
		lines = append(lines, line)
		line = word
	}
	lines = append(lines, line)
	return strings.Join(lines, "\n")
}

func (m *Model) renderIDEPreview(ide *IDEState, width int) string {
	if ide == nil || ide.Ed == nil || ide.Ed.Buffer() == nil {
		return "no preview"
	}
	content := safeIDEPlainMultilineText(ide.Ed.Buffer().String())
	if m.md != nil {
		content = m.md.Render(content, max(width, 20))
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	height := max(ide.UI.Height, 3)
	maxScroll := max(len(lines)-height, 0)
	ide.previewScroll = clamp(ide.previewScroll, 0, maxScroll)
	var out []string
	for i := ide.previewScroll; i < len(lines) && len(out) < height; i++ {
		out = append(out, clampANSIWidth(lines[i], max(width, 10)))
	}
	for len(out) < height {
		out = append(out, "")
	}
	return strings.Join(out, "\n")
}

// ideCodeTop is the screen row where the editor code area starts (tab bar +
// box top border + panel title).
func (m *Model) ideCodeTop() int { return tabBarRows + 2 }

// renderBufferTabs shows open buffers as a real editor tab strip. The active
// buffer receives the only accent surface in the IDE chrome.
func (m *Model) renderBufferTabs(ide *IDEState, width int) string {
	var b strings.Builder
	for i, buf := range ide.Ed.Buffers {
		name := safeIDEPlainText(bufferTabName(buf.Path))
		if buf.Dirty {
			name += " ●"
		}
		tab := lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(m.styles.T.Color(TokenSmoke))
		if i == ide.Ed.CurBuf {
			tab = lipgloss.NewStyle().
				Padding(0, 1).
				Foreground(m.styles.T.Color(TokenOyster)).
				Background(m.styles.T.Blend(TokenSurface, TokenCharple, 0.18)).
				Bold(true)
		}
		cell := tab.Render(truncateRunes(name, 24))
		if lipgloss.Width(b.String())+lipgloss.Width(cell) > width {
			break
		}
		b.WriteString(cell)
		b.WriteString(" ")
	}
	return clampANSIWidth(b.String(), max(width, 1))
}

func bufferTabName(path string) string {
	if path == "" || path == "untitled" {
		return "untitled"
	}
	return filepath.Base(path)
}

// renderIDEChat is the chat bar below the editor.
func (m *Model) renderIDEChat(ide *IDEState, width int) string {
	rail := m.styles.T.Color(TokenIron)
	if ide.Focus == ideChat {
		rail = m.styles.T.Color(TokenCharple)
	}
	dockW := min(max(width-4, 20), 112)
	inset := max((width-dockW)/2, 2)
	contentW := max(dockW-4, 1)
	promptMark := "·"
	if ide.Focus == ideChat {
		promptMark = "✦"
	}
	prompt := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenCharple)).Bold(true).Render(promptMark + "  ")
	input := ""
	if m.input.Value() == "" {
		input = m.styles.InputHint.Render(m.composerPlaceholder(true))
		if ide.Focus == ideChat {
			input += lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly)).Render(" ▏")
		}
	} else {
		input = m.input.sizedView(max(contentW-lipgloss.Width(prompt), 1), m.inputH)
		input = strings.ReplaceAll(input, "\n", "\n"+strings.Repeat(" ", lipgloss.Width(prompt)))
	}
	main := prompt + clampANSIWidth(input, max(contentW-lipgloss.Width(prompt), 1))
	selection := m.ideSelectionDescriptor(ide)
	actions, positions := m.renderComposerActions(contentW, selection)

	_, treeW, _ := m.idePaneWidths()
	actionY := tabBarRows + m.bodyHeight() - 2
	for _, action := range positions {
		m.regions = append(m.regions, Region{
			X: treeW + action.X + inset + 2, Y: actionY, W: action.W, H: 1,
			Action: action.Action, Label: action.Label, Binding: action.Binding,
		})
	}
	dock := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(rail).
		Background(m.styles.T.Color(TokenPanel)).
		Padding(0, 1).
		Width(dockW).MaxWidth(dockW).
		Render(main + "\n" + actions)
	return lipgloss.NewStyle().PaddingLeft(inset).Width(width).MaxWidth(width).Render(dock)
}

func (m *Model) ideSelectionDescriptor(ide *IDEState) string {
	if ide == nil || ide.Ed == nil {
		return ""
	}
	_, start, end, ok := ide.Ed.SelectionText()
	if !ok {
		return ""
	}
	name := "selection"
	if buf := ide.Ed.Buffer(); buf != nil {
		name = safeIDEPlainText(filepath.Base(buf.Path))
	}
	if start.Line == end.Line {
		return fmt.Sprintf("%s · line %d", name, start.Line+1)
	}
	return fmt.Sprintf("%s · lines %d–%d", name, start.Line+1, end.Line+1)
}

// renderTree draws a quiet, text-first explorer. Extensions already identify
// files; spelling out "dir", "go" and "md" wastes the narrowest pane.
func (m *Model) renderTree(width, height int) string {
	ide := m.ide
	entries := ide.treeEntries()
	lines := max(height-1, 1) // reserve the last row for the file count
	if ide.treeSel >= len(entries) {
		ide.treeSel = max(len(entries)-1, 0)
	}
	top := 0
	if ide.treeSel >= lines {
		top = ide.treeSel - lines + 1
	}
	var linesOut []string
	for i := top; i < len(entries) && i < top+lines; i++ {
		entry := entries[i]
		marker := "  "
		if entry.Dir {
			marker = "› "
			if ide.treeExpanded[entry.Path] {
				marker = "⌄ "
			}
		}
		style := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke))
		if i == ide.treeSel {
			style = lipgloss.NewStyle().
				Foreground(m.styles.T.Color(TokenOyster)).
				Background(m.styles.T.Blend(TokenSurface, TokenCharple, 0.16)).
				Width(max(width-1, 1)).MaxWidth(max(width-1, 1))
		}
		label := "  " + strings.Repeat("  ", entry.Depth) + marker + safeIDEPlainText(entry.Name)
		linesOut = append(linesOut, style.Render(truncateRunes(label, width)))
	}
	for len(linesOut) < lines {
		linesOut = append(linesOut, "")
	}
	ide.treeScroll.set(lines, lines, len(entries), top)
	treeBlock := strings.Join(linesOut, "\n")
	if bar := ide.treeScroll.View(m.styles); bar != "" {
		treeBlock = lipgloss.JoinHorizontal(lipgloss.Top, treeBlock, bar)
	}
	return treeBlock + "\n" + m.styles.Hint.Render(fmt.Sprintf("  %d files", len(ide.files())))
}

func (m *Model) renderChangedTree(width, height int) string {
	rows := max(height-1, 1)
	lines := make([]string, 0, rows+1)
	if len(m.sidebar.modFiles) == 0 {
		lines = append(lines, m.styles.Hint.Render("  working tree clean"))
	} else {
		for _, file := range m.sidebar.modFiles {
			if len(lines) >= rows {
				break
			}
			mark := "M"
			color := m.styles.T.Color(TokenMustard)
			if file.Untracked {
				mark, color = "U", m.styles.T.Color(TokenJulep)
			}
			prefix := lipgloss.NewStyle().Foreground(color).Bold(true).Render(mark)
			lines = append(lines, clampANSIWidth("  "+prefix+"  "+safeIDEPlainText(file.Path), width))
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n" + m.styles.Hint.Render("  click to open")
}

func truncateRunes(s string, w int) string {
	runes := []rune(s)
	if len(runes) <= w {
		return s
	}
	return string(runes[:max(w-1, 0)]) + "…"
}
