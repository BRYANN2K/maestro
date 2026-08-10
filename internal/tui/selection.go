package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/editor"
)

func (m *Model) openIDESelectionMenu(x, y int) bool {
	if m.ide == nil {
		return false
	}
	text, start, end, ok := m.ide.Ed.SelectionText()
	if !ok || text == "" {
		return false
	}
	selection := &selectionContext{
		Source: "ide",
		Path:   m.ide.Ed.Buffer().Path,
		Start:  start,
		End:    end,
		Text:   text,
	}
	m.pendingSelection = selection
	m.selectionMenu = newSelectionMenu(selection, x+1, y+1)
	m.selectionOverlayX = x + 1
	m.selectionOverlayY = y + 1
	return true
}

func (m *Model) chatPointAt(x, y int) (chatPoint, bool) {
	top := tabBarRows // the transcript starts immediately below the tab bar
	if y < top || y >= top+m.viewport.Height {
		return chatPoint{}, false
	}
	row := m.viewport.YOffset + y - top
	if row < 0 || row >= len(m.chatRows) || m.chatRows[row].Message == nil || m.chatRows[row].TextLine < 0 {
		return chatPoint{}, false
	}
	line := m.chatRows[row].Message.Text
	parts := strings.Split(line, "\n")
	textLine := m.chatRows[row].TextLine
	if textLine >= len(parts) {
		textLine = len(parts) - 1
	}
	col := max(x-1, 0)
	if textLine >= 0 && textLine < len(parts) {
		col = min(col, len([]rune(parts[textLine])))
	}
	return chatPoint{Row: row, Col: col}, true
}

func (m *Model) chatSelectionContext() *selectionContext {
	if !m.chatSelecting || len(m.chatRows) == 0 {
		return nil
	}
	a, b := m.chatAnchor, m.chatCursor
	if a.Row > b.Row || (a.Row == b.Row && a.Col > b.Col) {
		a, b = b, a
	}
	if a.Row < 0 || b.Row >= len(m.chatRows) {
		return nil
	}
	startRow, endRow := m.chatRows[a.Row], m.chatRows[b.Row]
	if startRow.Message == nil || endRow.Message == nil || startRow.Message != endRow.Message {
		return nil
	}
	parts := strings.Split(startRow.Message.Text, "\n")
	startLine, endLine := startRow.TextLine, endRow.TextLine
	if startLine < 0 || endLine < 0 || startLine >= len(parts) || endLine >= len(parts) {
		return nil
	}
	if startLine > endLine || (startLine == endLine && a.Col > b.Col) {
		startLine, endLine, a.Col, b.Col = endLine, startLine, b.Col, a.Col
	}
	var text string
	if startLine == endLine {
		runes := []rune(parts[startLine])
		text = string(runes[min(a.Col, len(runes)):min(b.Col, len(runes))])
	} else {
		lines := []string{string([]rune(parts[startLine])[min(a.Col, len([]rune(parts[startLine]))):])}
		lines = append(lines, parts[startLine+1:endLine]...)
		last := []rune(parts[endLine])
		lines = append(lines, string(last[:min(b.Col, len(last))]))
		text = strings.Join(lines, "\n")
	}
	return &selectionContext{
		Source:  "chat",
		Path:    "chat",
		Start:   editor.Cursor{Line: startLine, Col: a.Col},
		End:     editor.Cursor{Line: endLine, Col: b.Col},
		Text:    text,
		Message: startRow.Message,
	}
}

func (m *Model) activeChatSelection(msg *Message) *selectionContext {
	if m.chatSelecting {
		if selection := m.chatSelectionContext(); selection != nil && selection.MessageMatches(msg) {
			return selection
		}
	}
	if m.pendingSelection != nil && m.pendingSelection.Source == "chat" && m.pendingSelection.MessageMatches(msg) {
		return m.pendingSelection
	}
	return nil
}

func (s *selectionContext) MessageMatches(msg *Message) bool {
	return s != nil && msg != nil && s.Source == "chat" && s.Path == "chat" && (s.Message == nil || s.Message == msg)
}

func chatCellSelected(selection *selectionContext, line, col int) bool {
	if selection == nil || line < selection.Start.Line || line > selection.End.Line {
		return false
	}
	if selection.Start.Line == selection.End.Line {
		return col >= selection.Start.Col && col < selection.End.Col
	}
	if line == selection.Start.Line {
		return col >= selection.Start.Col
	}
	if line == selection.End.Line {
		return col < selection.End.Col
	}
	return true
}

func (m *Model) updateSelectionMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	menu := m.selectionMenu
	if menu == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.closeSelectionMenu()
	case tea.KeyUp:
		if menu.Selected > 0 {
			menu.Selected--
		}
	case tea.KeyDown:
		if menu.Selected < len(menu.Actions)-1 {
			menu.Selected++
		}
	case tea.KeyEnter:
		return m, m.activateSelectionAction(menu.Selected)
	case tea.KeyRunes:
		keys := map[string]string{"e": "edit selection", "r": "add to context", "x": "explain", "m": "modify with Maestro", "c": "comment", "a": "ask Maestro…"}
		if action, ok := keys[strings.ToLower(msg.String())]; ok {
			for i, candidate := range menu.Actions {
				if candidate == action {
					return m, m.activateSelectionAction(i)
				}
			}
		}
		if menu.Context != nil && menu.Context.Source == "ide" && len(msg.Runes) > 0 {
			// A selected code range behaves like a normal editor selection:
			// typing any other character opens replacement editing and
			// inserts that character immediately.
			m.activateSelectionAction(0)
			return m.updateSelectionEditKey(msg)
		}
	}
	return m, nil
}

func (m *Model) updateSelectionEditKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.selectionEdit == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.selectionEdit = nil
		m.selectionMenu = nil
		m.pendingSelection = nil
		return m, nil
	case tea.KeyEnter:
		if msg.Alt {
			m.selectionEdit.replaceIfArmed()
			m.selectionEdit.insertNewline()
			return m, nil
		}
		return m, m.commitSelectionEdit()
	case tea.KeyCtrlS:
		return m, m.commitSelectionEdit()
	default:
		m.selectionEdit.update(msg)
	}
	return m, nil
}

// updateSelectionAskKey handles the inline question editor shown above a
// selected IDE/chat range. Enter sends the question with the selected text as
// context; Shift+Enter keeps the question multiline (Bubble Tea exposes that
// modified Enter form through KeyMsg.Alt on supported terminals).
func (m *Model) updateSelectionAskKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.selectionAsk == nil {
		return m, nil
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.selectionAsk = nil
		m.selectionAskCtx = nil
		m.pendingSelection = nil
		return m, nil
	case tea.KeyEnter:
		if msg.Alt {
			m.selectionAsk.insertNewline()
			return m, nil
		}
		if strings.TrimSpace(m.selectionAsk.Value()) == "" {
			m.status.pushToast("info", "write a question first", 2*time.Second)
			return m, nil
		}
		return m, m.submitSelectionAsk()
	case tea.KeyCtrlS:
		if strings.TrimSpace(m.selectionAsk.Value()) == "" {
			return m, nil
		}
		return m, m.submitSelectionAsk()
	default:
		m.selectionAsk.update(msg)
	}
	return m, nil
}

func (m *Model) submitSelectionAsk() tea.Cmd {
	if m.selectionAsk == nil || m.selectionAskCtx == nil {
		return nil
	}
	question := strings.TrimSpace(m.selectionAsk.Value())
	selection := m.selectionAskCtx
	m.selectionAsk = nil
	m.selectionAskCtx = nil
	m.pendingSelection = nil
	m.switchTab(TabHarness)
	m.input.Set(selectionQuestionPrompt(selection, question))
	m.focus = FocusInput
	return m.send()
}

func (m *Model) activateSelectionAction(index int) tea.Cmd {
	menu := m.selectionMenu
	if menu == nil || menu.Context == nil || index < 0 || index >= len(menu.Actions) {
		m.closeSelectionMenu()
		return nil
	}
	action := menu.Actions[index]
	selection := menu.Context
	switch action {
	case "add to context":
		m.contextRefs = append(m.contextRefs, *selection)
		m.closeSelectionMenu()
		m.status.pushToast("success", fmt.Sprintf("context added · %d selection(s)", len(m.contextRefs)), 2*time.Second)
		if m.activeTab == TabIDE && m.ide != nil {
			m.ide.Focus = ideChat
		} else {
			m.focus = FocusInput
		}
		return nil
	case "edit selection":
		m.selectionEdit = newInputBox(m.styles)
		m.selectionEdit.setWidth(min(max(m.width-10, 10), 96))
		m.selectionEdit.Set(selection.Text)
		m.selectionEdit.armReplacement()
		m.selectionOverlayX = menu.X
		m.selectionOverlayY = menu.Y
		m.selectionMenu = nil
		return nil
	case "ask Maestro…":
		m.selectionMenu = nil
		m.selectionAskCtx = selection
		m.selectionAsk = newInputBox(m.styles)
		m.selectionAsk.setWidth(min(max(m.width-10, 10), 76))
		m.selectionAsk.Set("")
		m.selectionOverlayX = menu.X
		m.selectionOverlayY = menu.Y
		return nil
	case "explain", "modify with Maestro", "comment":
		promptAction := action
		if action == "modify with Maestro" {
			promptAction = "modify"
		}
		prompt := selectionPrompt(promptAction, selection)
		m.closeSelectionMenu()
		m.switchTab(TabHarness)
		m.input.Set(prompt)
		m.focus = FocusInput
		return m.send()
	}
	m.closeSelectionMenu()
	return nil
}

func (m *Model) commitSelectionEdit() tea.Cmd {
	if m.selectionEdit == nil {
		return nil
	}
	text := m.selectionEdit.Value()
	m.selectionEdit = nil
	m.selectionMenu = nil
	m.pendingSelection = nil
	if m.ide == nil || !m.ide.Ed.ReplaceSelection(text) {
		return nil
	}
	if b := m.ide.Ed.Buffer(); b != nil {
		m.ide.Ed.Status = "selection replaced"
		if m.ide.UI.Gutter != nil {
			m.ide.UI.Gutter.Refresh(m.ctx(), b.Path)
		}
	}
	m.ide.Focus = ideEditor
	return nil
}

func (m *Model) closeSelectionMenu() {
	m.selectionMenu = nil
	m.pendingSelection = nil
}

func (m *Model) selectionActionAt(x, y int) (int, bool) {
	if m.selectionMenu == nil {
		return 0, false
	}
	box := m.styles.Dialog.Render(m.renderSelectionMenu())
	width := lipgloss.Width(box)
	lines := strings.Split(box, "\n")
	plainLines := strings.Split(ansi.Strip(box), "\n")
	for i := range lines {
		if y == m.selectionOverlayY+i && x >= m.selectionOverlayX && x < m.selectionOverlayX+width {
			if i < len(plainLines) {
				line := strings.TrimSpace(plainLines[i])
				for index, action := range m.selectionMenu.Actions {
					if strings.Contains(line, action) {
						return index, true
					}
				}
			}
		}
	}
	return 0, false
}

func (m *Model) renderSelectionMenu() string {
	if m.selectionMenu == nil {
		return ""
	}
	menu := m.selectionMenu
	var b strings.Builder
	b.WriteString(m.styles.DialogTitle("Selection") + "\n")
	if menu.Context != nil {
		preview := safeIDEPlainText(strings.Join(strings.Fields(menu.Context.Text), " "))
		if len([]rune(preview)) > 42 {
			preview = string([]rune(preview)[:41]) + "…"
		}
		path := truncateIDEPlainText(menu.Context.Path, 42)
		b.WriteString(m.styles.Hint.Render(fmt.Sprintf("%s · %q", path, preview)) + "\n\n")
	}
	for i, action := range menu.Actions {
		marker := "  "
		style := m.styles.SidebarItem
		if i == menu.Selected {
			marker = "▸ "
			style = m.styles.SidebarActive
		}
		b.WriteString(style.Render(marker+action) + "\n")
	}
	b.WriteString("\n" + m.styles.Hint.Render("↑/↓ · enter · esc"))
	return b.String()
}

func (m *Model) renderSelectionEdit() string {
	if m.selectionEdit == nil {
		return ""
	}
	width := min(max(m.width-10, 10), 96)
	var b strings.Builder
	b.WriteString(m.styles.DialogTitle("Edit selection") + "\n")
	b.WriteString(m.styles.Hint.Render("replace the selected text") + "\n\n")
	b.WriteString(m.selectionEdit.view(width-4) + "\n\n")
	b.WriteString(m.styles.Hint.Render("enter apply · shift+enter newline · esc cancel"))
	return b.String()
}

func (m *Model) renderSelectionAsk() string {
	if m.selectionAsk == nil || m.selectionAskCtx == nil {
		return ""
	}
	width := min(max(m.width-10, 10), 76)
	var b strings.Builder
	b.WriteString(m.styles.DialogTitle("Ask about selection") + "\n")
	preview := selectionPreview(m.selectionAskCtx.Text, max(width-4, 1))
	path := truncateIDEPlainText(m.selectionAskCtx.Path, max(width/2, 12))
	b.WriteString(m.styles.Hint.Render(fmt.Sprintf("%s · %q", path, preview)) + "\n\n")
	b.WriteString(m.styles.PanelTitle("Question") + "\n")
	content := m.selectionAsk.view(max(width-4, 1))
	if strings.TrimSpace(m.selectionAsk.Value()) == "" {
		content = m.styles.InputHint.Render("What would you like to know?")
	}
	b.WriteString(m.styles.InputFocus.Width(max(width-4, 1)).MaxWidth(max(width-4, 1)).Render(content) + "\n\n")
	b.WriteString(m.styles.Hint.Render("enter send · shift+enter newline · esc cancel"))
	return b.String()
}

func selectionPreview(text string, width int) string {
	preview := safeIDEPlainText(strings.Join(strings.Fields(text), " "))
	if len([]rune(preview)) > width {
		preview = string([]rune(preview)[:max(width-1, 1)]) + "…"
	}
	return preview
}

// selectionOverlayPosition keeps the recorded screen coordinates in sync
// with the actual clamped placement. Mouse clicks therefore keep working
// when a selection is close to the bottom or right edge of the terminal.
func (m *Model) selectionOverlayPosition(box string, bodyHeight int) (int, int) {
	boxWidth := lipgloss.Width(box)
	boxHeight := len(strings.Split(box, "\n"))
	x := clamp(m.selectionOverlayX, 0, max(m.width-boxWidth, 0))
	y := clamp(m.selectionOverlayY-tabBarRows, 0, max(bodyHeight-boxHeight, 0))
	m.selectionOverlayX = x
	m.selectionOverlayY = y + tabBarRows
	return x, y
}

// overlayAt composites a small ANSI-aware floating panel over an existing
// frame. It keeps the editor/chat frame visible, unlike a full-screen modal.
func overlayAt(base string, width, height, x, y int, overlay string) string {
	if overlay == "" || width <= 0 || height <= 0 {
		return base
	}
	lines := strings.Split(base, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	if lipgloss.Height(overlay) > height {
		overlay = fitANSIHeight(overlay, height)
	}
	overlayLines := strings.Split(overlay, "\n")
	boxWidth := lipgloss.Width(overlay)
	boxHeight := len(overlayLines)
	x = clamp(x, 0, max(width-boxWidth, 0))
	y = clamp(y, 0, max(height-boxHeight, 0))
	for i, line := range overlayLines {
		row := lines[y+i]
		prefix := ansi.Cut(row, 0, x)
		suffix := ansi.Cut(row, x+boxWidth, width)
		lines[y+i] = prefix + "\x1b[0m" + line + "\x1b[0m" + suffix
		lines[y+i] = clampANSIWidth(lines[y+i], width)
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
