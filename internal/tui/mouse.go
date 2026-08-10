package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/projectprofile"
)

// Region is one clickable screen area, filled at render time and hit-tested
// by the mouse dispatcher. One action, two triggers (§5.10.3).
type Region struct {
	X, Y, W, H   int
	Action       ActionID
	CardID       string
	Index        int
	Tab          Tab
	ResizeTarget int
	Target       string
	Line         int
	Column       int
	Label        string
	Binding      string
}

// contains reports whether (x, y) falls inside the region.
func (r Region) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// updateMouse routes mouse messages through the registered regions.
func (m *Model) updateMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	// The tiny-terminal recovery screen deliberately hides every interactive
	// surface. Never dispatch stale dialog, overlay, transcript or IDE hitboxes
	// behind it: a click on the fallback must not approve a permission or
	// change a model route invisibly.
	if m.terminalTooSmall() {
		return m, nil
	}
	x, y := msg.X, msg.Y
	if dialog, ok := m.dialogs.top(); ok {
		permission, ok := dialog.(*permissionDialog)
		if !ok {
			return m, nil
		}
		if index, hit := permission.buttonAt(x, y); hit {
			permission.buttonSel = index
			if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
				m.finishPermission(permission, permission.resolve())
			}
		}
		return m, nil
	}
	if m.overlay != overlayNone {
		switch overlay := m.overlayM.(type) {
		case *settingsOverlay:
			return m, overlay.mouse(m, msg)
		case *taskModelOverlay:
			return m, overlay.mouse(m, msg)
		case *providersOverlay:
			return m, overlay.mouse(m, msg)
		case *diffOverlay:
			return m.updateDiffOverlayMouse(overlay, msg)
		}
		// Generic pickers, forms and Coach currently have no mouse hit map.
		// They still own the whole modal surface: never let a click fall through
		// to stale chat/IDE regions behind an overlay.
		return m, nil
	}
	if m.selectionEdit != nil || m.selectionAsk != nil {
		return m, nil
	}
	if m.selectionMenu != nil {
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			if index, ok := m.selectionActionAt(x, y); ok {
				return m, m.activateSelectionAction(index)
			}
			m.closeSelectionMenu()
		}
		return m, nil
	}
	if m.activeTab == TabIDE && m.ide != nil {
		if m.ide.mouseSelecting {
			_, treeW, _ := m.idePaneWidths()
			switch msg.Action {
			case tea.MouseActionMotion:
				m.ide.UI.Ed.UpdateSelectionCursor(m.ide.UI.CursorAt(x-treeW, y-m.ideCodeTop()))
				m.ide.mouseMoved = true
				return m, nil
			case tea.MouseActionRelease:
				m.ide.mouseSelecting = false
				if !m.ide.mouseMoved {
					m.ide.UI.Ed.CancelSelection()
				} else {
					m.openIDESelectionMenu(x, y)
				}
				return m, nil
			}
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			editorW, treeW, _ := m.idePaneWidths()
			codeTop := m.ideCodeTop()
			codeBottom := codeTop + max(m.ide.UI.Height, m.bodyHeight()-8)
			if x >= treeW && x < treeW+editorW && y >= codeTop && y < codeBottom {
				m.ide.UI.Ed.BeginSelectionAt(m.ide.UI.CursorAt(x-treeW, y-codeTop))
				m.ide.mouseSelecting = true
				m.ide.mouseMoved = false
				m.ide.Focus = ideEditor
				return m, nil
			}
		}
	}
	if m.activeTab == TabHarness {
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			for _, region := range m.regions {
				if region.Action == ActionOpenPath && region.contains(x, y) {
					return m.dispatchRegionWithCmd(region)
				}
			}
		}
		if m.chatSelecting {
			switch msg.Action {
			case tea.MouseActionMotion:
				if point, ok := m.chatPointAt(x, y); ok {
					m.chatCursor = point
				}
				return m, nil
			case tea.MouseActionRelease:
				if point, ok := m.chatPointAt(x, y); ok {
					m.chatCursor = point
				}
				selection := m.chatSelectionContext()
				m.chatSelecting = false
				if selection != nil && selection.Text != "" && (m.chatAnchor != m.chatCursor) {
					m.pendingSelection = selection
					m.selectionMenu = newSelectionMenu(selection, x+1, y+1)
					m.selectionOverlayX, m.selectionOverlayY = x+1, y+1
				} else {
					m.pendingSelection = nil
				}
				return m, nil
			}
		}
		if msg.Button == tea.MouseButtonLeft && msg.Action == tea.MouseActionPress {
			if point, ok := m.chatPointAt(x, y); ok {
				m.chatSelecting = true
				m.chatAnchor = point
				m.chatCursor = point
				m.focus = FocusViewport
				return m, nil
			}
		}
	}
	if m.resizing {
		switch msg.Action {
		case tea.MouseActionMotion:
			m.resizeIDEAt(x, y)
			return m, nil
		case tea.MouseActionRelease:
			m.resizing = false
			return m, nil
		}
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		if m.activeTab == TabIDE && m.ide != nil {
			m.handleIDEWheel(x, y, -3)
			return m, nil
		}
		if m.showActivityRail() {
			chatW, _ := m.harnessPaneWidths()
			if x >= chatW {
				m.sidebar.moveSelection(-1)
				return m, nil
			}
		}
		m.followOutput = false
		m.viewport.ScrollUp(3)
		if m.tailMode {
			m.renderMessages()
		}
		return m, nil
	case tea.MouseButtonWheelDown:
		if m.activeTab == TabIDE && m.ide != nil {
			m.handleIDEWheel(x, y, 3)
			return m, nil
		}
		if m.showActivityRail() {
			chatW, _ := m.harnessPaneWidths()
			if x >= chatW {
				m.sidebar.moveSelection(1)
				return m, nil
			}
		}
		m.viewport.ScrollDown(3)
		if m.tailMode {
			m.renderMessages()
		}
		if m.viewport.AtBottom() {
			m.followOutput = true
		}
		return m, nil
	case tea.MouseButtonLeft:
		// AllMotion delivers motion/release events with the left button
		// held; only presses dispatch semantic actions.
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		for _, r := range m.regions {
			if r.contains(x, y) {
				if r.Action == ActionResize {
					m.beginResize(r.ResizeTarget, x, y)
					return m, nil
				}
				if r.Action == ActionSend {
					if strings.TrimSpace(m.input.Value()) == "" || m.busy {
						return m, nil
					}
					return m, m.send()
				}
				return m.dispatchRegionWithCmd(r)
			}
		}
		// Click into the harness input area: focus it. In the IDE the
		// editor/tree/HITL panes keep their own focus.
		if m.activeTab == TabIDE && m.ide != nil {
			m.focusIDEPaneAt(x, y)
		} else {
			m.focus = FocusInput
		}
		return m, nil
	default:
		// Hover (MouseButtonNone): show the action + its keybinding.
		msg := ""
		for _, r := range m.regions {
			if r.contains(x, y) {
				binding := r.Binding
				if binding == "" {
					binding = KeyFor(r.Action)
				}
				msg = fmt.Sprintf("%s — %s", r.Label, binding)
				break
			}
		}
		m.hoverMsg = msg
		return m, nil
	}
}

type inputFilter struct {
	lastMotion     time.Time
	noisePending   bool
	noisePendingAt time.Time
}

// InputFilter runs before Bubble Tea queues messages. It drops malformed key
// fragments and coalesces hover motion so a noisy terminal adapter cannot
// starve real keyboard input or leak SGR reports into the text widgets.
func InputFilter() func(tea.Model, tea.Msg) tea.Msg {
	f := &inputFilter{}
	return f.filter
}

func (f *inputFilter) filter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch typed := msg.(type) {
	case tea.KeyMsg:
		if typed.Paste {
			f.noisePending = false
			f.noisePendingAt = time.Time{}
			return typed
		}
		text := string(typed.Runes)
		if f.noisePending {
			if !f.noisePendingAt.IsZero() && time.Since(f.noisePendingAt) > 100*time.Millisecond {
				f.noisePending = false
				f.noisePendingAt = time.Time{}
			} else {
				if strings.ContainsAny(text, "Mm") {
					f.noisePending = false
					f.noisePendingAt = time.Time{}
				}
				return nil
			}
		}
		if terminalNoiseKey(typed) {
			if (strings.HasPrefix(text, "[<") || strings.HasPrefix(text, "<")) && !strings.ContainsAny(text, "Mm") {
				f.noisePending = true
				f.noisePendingAt = time.Now()
			}
			return nil
		}
		// A paste or coalesced key event may contain useful text around one or
		// more leaked mouse reports. Preserve the useful text and remove only
		// the complete reports.
		if clean := stripTerminalReports(text); clean != text {
			if clean == "" {
				return nil
			}
			typed.Runes = []rune(clean)
			typed.Alt = false
			return typed
		}
	case tea.MouseMsg:
		if typed.Action == tea.MouseActionMotion {
			now := time.Now()
			if !f.lastMotion.IsZero() && now.Sub(f.lastMotion) < 16*time.Millisecond {
				return nil
			}
			f.lastMotion = now
		}
	}
	return msg
}

// registerViewportCardRegions maps card action hints to screen coordinates.
func (m *Model) registerViewportCardRegions() {
	seen := map[string]bool{}
	for _, msg := range m.messages {
		for _, card := range msg.Cards {
			if card == nil || seen[card.ID] {
				continue
			}
			seen[card.ID] = true
			row, ok := m.cardRows[card.ID]
			if !ok {
				continue
			}
			y := tabBarRows + row - m.viewport.YOffset
			if y < 1 || y >= m.viewport.Height+1 {
				continue
			}
			m.registerCardRegions(card, y)
		}
	}
	for target, row := range m.blockRows {
		y := tabBarRows + row - m.viewport.YOffset
		if y < 1 || y >= m.viewport.Height+1 {
			continue
		}
		m.regions = append(m.regions, Region{
			X: 2, Y: y, W: m.viewport.Width - 2, H: 1,
			Action: ActionToggleCode, Target: target,
			Label: "toggle code block", Binding: "click or v",
		})
	}
	for idx, row := range m.thinkRows {
		y := tabBarRows + row - m.viewport.YOffset
		if y < 1 || y >= m.viewport.Height+1 {
			continue
		}
		m.regions = append(m.regions, Region{
			X: 2, Y: y, W: m.viewport.Width - 2, H: 1,
			Action: ActionToggleThink, Index: idx,
			Label: "toggle working summary", Binding: "click or t",
		})
	}
}

// registerCardRegions records the clickable areas of one card's buttons at
// its viewport row (content coordinates).
func (m *Model) registerCardRegions(c *Card, row int) {
	height := lipgloss.Height(c.Render(m.styles, m.viewport.Width-2))
	top := max(row-height+1, tabBarRows)
	if c.Status != "proposed" {
		if c.Status == "error" {
			m.regions = append(m.regions, Region{
				X: 3, Y: row, W: 20, H: 1, Action: ActionAskFix, CardID: c.ID,
				Label: "ask Maestro to fix this error", Binding: "click",
			})
		}
		m.regions = append(m.regions, Region{
			X: 2, Y: top, W: max(m.viewport.Width-2, 1), H: max(height, 1),
			Action: ActionToggleCard, CardID: c.ID, Label: "toggle tool output", Binding: "click",
		})
		return
	}
	m.regions = append(m.regions,
		Region{X: 2, Y: row, W: 9, H: 1, Action: ActionAccept, CardID: c.ID, Label: "accept " + c.ProposalPath},
		Region{X: 13, Y: row, W: 9, H: 1, Action: ActionDiscard, CardID: c.ID, Label: "discard " + c.ProposalPath},
		Region{X: 2, Y: top, W: max(m.viewport.Width-2, 1), H: max(height-1, 1), Action: ActionToggleCard, CardID: c.ID, Label: "toggle document diff", Binding: "click"},
		Region{X: 24, Y: row, W: max(m.viewport.Width-26, 10), H: 1, Action: ActionToggleCard, CardID: c.ID, Label: "toggle document diff", Binding: "click"},
	)
}

func (m *Model) prepareFixPrompt(cardID string) tea.Model {
	for _, message := range m.messages {
		for _, card := range message.Cards {
			if card == nil || card.ID != cardID {
				continue
			}
			detail := strings.TrimSpace(card.Detail)
			if output := strings.TrimSpace(card.Full); output != "" && output != detail {
				detail += "\n\n" + truncateRunes(output, 8000)
			}
			m.switchTab(TabHarness)
			m.input.Set(fmt.Sprintf("Fix the failure from `%s`. Diagnose the root cause, make the smallest safe change, and run the relevant verification.\n\n%s", card.Name, detail))
			m.inputChanged()
			m.focus = FocusInput
			return m
		}
	}
	return m
}

// registerSidebarRegions makes agent rows clickable for the drill-down
// detail and HITL rows clickable to toggle.
func (m *Model) registerSidebarRegions() {
	chatW, rightW := m.harnessPaneWidths()
	x := chatW + 1
	for i := m.sidebar.scroll; i < len(m.sidebar.hitl); i++ {
		rel, ok := m.sidebar.hitlRows[i]
		if !ok {
			continue
		}
		y := tabBarRows + rel
		if y >= tabBarRows+m.bodyHeight()-1 {
			break
		}
		m.regions = append(m.regions, Region{
			X: x, Y: y, W: min(rightW, 4), H: 1,
			Action: ActionToggleHITL, Index: i,
			Label: "toggle human action", Binding: "click or space",
		})
		if raw := workspaceFilePattern.FindString(m.sidebar.hitl[i].Item); raw != "" {
			path, line, column := parseWorkspaceLocation(raw)
			m.regions = append(m.regions, Region{
				X: x + 4, Y: y, W: max(rightW-4, 1), H: 1,
				Action: ActionOpenPath, Target: path, Line: line, Column: column,
				Label: "open task location " + raw, Binding: "click",
			})
		} else {
			m.regions[len(m.regions)-1].W = rightW
		}
	}
	for i, role := range m.sidebar.agentKeys {
		rel, ok := m.sidebar.agentRows[i]
		if !ok {
			continue
		}
		m.regions = append(m.regions, Region{
			X: x, Y: tabBarRows + rel, W: rightW, H: 1,
			Action: ActionAgentDetail, Index: i,
			Label: "agent " + role, Binding: "click",
		})
	}
	for i, file := range m.sidebar.modFiles {
		rel, ok := m.sidebar.fileRows[i]
		if !ok {
			continue
		}
		m.regions = append(m.regions, Region{
			X: x, Y: tabBarRows + rel, W: rightW, H: 1,
			Action: ActionOpenPath, Target: file.Path,
			Label: "open " + file.Path + " in IDE", Binding: "click",
		})
	}
	if m.sidebar.coach != nil && m.sidebar.coachRow >= 0 {
		m.regions = append(m.regions, Region{
			X: x, Y: tabBarRows + m.sidebar.coachRow, W: rightW, H: 2,
			Action: ActionCoachOpen, Label: "open Maestro Coach", Binding: "click or /learn next",
		})
	}
}

var workspaceFilePattern = regexp.MustCompile(`(?:/?(?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+\.(?:go|py|rs|java|rb|php|ts|tsx|js|jsx|css|html|sql|sh|md|txt|json|toml|yaml|yml|mod|sum))(?:#L[0-9]+|:[0-9]+(?::[0-9]+)?)?`)

// parseWorkspaceLocation splits the common diagnostics/link formats
// file.go:12:4 and file.go#L12 without confusing the suffix with the path.
// Line and column are returned 1-based; zero means unspecified.
func parseWorkspaceLocation(raw string) (path string, line, column int) {
	raw = strings.Trim(strings.TrimSpace(raw), "`'\"()[]{}.,;")
	if marker := strings.LastIndex(raw, "#L"); marker >= 0 {
		path = raw[:marker]
		_, _ = fmt.Sscanf(raw[marker+2:], "%d", &line)
		return path, line, 0
	}
	path = raw
	parts := strings.Split(raw, ":")
	if len(parts) >= 2 {
		var parsedLine, parsedCol int
		if _, err := fmt.Sscanf(parts[len(parts)-1], "%d", &parsedCol); err == nil {
			if len(parts) >= 3 {
				if _, err := fmt.Sscanf(parts[len(parts)-2], "%d", &parsedLine); err == nil {
					return strings.Join(parts[:len(parts)-2], ":"), parsedLine, parsedCol
				}
			}
			return strings.Join(parts[:len(parts)-1], ":"), parsedCol, 0
		}
	}
	return path, 0, 0
}

// registerTranscriptFileRegions turns workspace-relative paths visible in
// the transcript into IDE links. It scans only the already-normalized visible
// rows, so scrolling remains independent of transcript size.
func (m *Model) registerTranscriptFileRegions() {
	if m.activeTab != TabHarness || len(m.transcriptLines) == 0 {
		return
	}
	top := clamp(m.viewport.YOffset, 0, max(len(m.transcriptLines)-1, 0))
	for screenRow := 0; screenRow < m.viewport.Height; screenRow++ {
		row := top + screenRow
		if row >= len(m.transcriptLines) {
			break
		}
		plain := ansi.Strip(m.transcriptLines[row])
		for _, match := range workspaceFilePattern.FindAllStringIndex(plain, -1) {
			raw := plain[match[0]:match[1]]
			path, line, column := parseWorkspaceLocation(raw)
			x := utf8.RuneCountInString(plain[:match[0]])
			w := utf8.RuneCountInString(raw)
			m.regions = append(m.regions, Region{
				X: x, Y: tabBarRows + screenRow, W: max(w, 1), H: 1,
				Action: ActionOpenPath, Target: path, Line: line, Column: column,
				Label: "open " + raw + " in IDE", Binding: "click",
			})
		}
	}
}

func (m *Model) dispatchRegionWithCmd(r Region) (tea.Model, tea.Cmd) {
	model := m.dispatchRegion(r)
	switch r.Action {
	case ActionAccept, ActionAcceptHunk, ActionDiscardHunk:
		return model, m.refreshModifiedFiles()
	default:
		return model, nil
	}
}

func (m *Model) dispatchRegion(r Region) tea.Model {
	switch r.Action {
	case ActionSwitchTab:
		m.switchTab(r.Tab)
	case ActionOpenFile:
		if m.activeTab == TabIDE && m.ide != nil {
			entries := m.ide.treeEntries()
			if r.Index >= 0 && r.Index < len(entries) {
				entry := entries[r.Index]
				if entry.Dir {
					m.ide.toggleTree(entry.Path)
				} else {
					m.ide.OpenFileAt(entry.Path)
					m.followAgent = false
				}
				m.ide.Focus = ideTree
			}
		}
	case ActionSwitchBuffer:
		if m.activeTab == TabIDE && m.ide != nil && r.Index >= 0 && r.Index < len(m.ide.Ed.Buffers) {
			m.ide.Ed.CurBuf = r.Index
			m.ide.Focus = ideEditor
			m.ide.previewScroll = 0
			m.followAgent = false
			if b := m.ide.Ed.Buffer(); b != nil && m.ide.UI.Gutter != nil {
				m.ide.UI.Gutter.Refresh(m.ctx(), b.Path)
			}
		}
	case ActionSwitchExplorer:
		if m.activeTab == TabIDE && m.ide != nil {
			if r.Target == "changes" {
				m.ide.explorerView = ideExplorerChanges
			} else {
				m.ide.explorerView = ideExplorerFiles
			}
			m.ide.Focus = ideTree
		}
	case ActionOpenChanged:
		if m.activeTab == TabIDE && m.ide != nil && r.Index >= 0 && r.Index < len(m.sidebar.modFiles) {
			m.ide.OpenFileAt(m.sidebar.modFiles[r.Index].Path)
			m.followAgent = false
		}
	case ActionOpenPath:
		m.openWorkspaceLocation(r.Target, r.Line, r.Column, true)
		return m
	case ActionAskFix:
		return m.prepareFixPrompt(r.CardID)
	case ActionToggleFollow:
		m.followAgent = !m.followAgent
		state := "FREE"
		if m.followAgent {
			state = "FOLLOW"
		}
		m.status.pushToast("info", "IDE navigation: "+state, 2*time.Second)
		return m
	case ActionOpenProposalIDE:
		for _, card := range m.pending {
			if card.ID == r.CardID {
				m.openProposalInIDE(card.Proposal)
				break
			}
		}
		return m
	case ActionPrevProposal:
		return m.cycleProposal(-1)
	case ActionNextProposal:
		return m.cycleProposal(1)
	case ActionPrevHunk:
		return m.cycleProposalHunk(-1)
	case ActionNextHunk:
		return m.cycleProposalHunk(1)
	case ActionAcceptHunk:
		return m.decideProposalHunk(true)
	case ActionDiscardHunk:
		return m.decideProposalHunk(false)
	case ActionAddContext:
		value := m.input.Value()
		if value != "" && !strings.HasSuffix(value, " ") {
			value += " "
		}
		m.input.Set(value + "@")
		m.inputChanged()
		if m.activeTab == TabIDE && m.ide != nil {
			m.ide.Focus = ideChat
		} else {
			m.focus = FocusInput
		}
		m.maybeOpenAtFile()
		return m
	case ActionPalette:
		m.overlay = overlayPalette
		m.overlayM = newPaletteOverlay(m.orch)
		return m
	case ActionKeymap:
		m.overlay = overlayKeymap
		return m
	case ActionIDESelection:
		if m.ide != nil {
			editorW, treeW, _ := m.idePaneWidths()
			m.openIDESelectionMenu(treeW+editorW/2, tabBarRows+m.bodyHeight()-2)
		}
		return m
	case ActionFocusIDEChat:
		if m.activeTab == TabIDE && m.ide != nil {
			m.ide.Focus = ideChat
		}
	case ActionAccept:
		return m.acceptCard(r)
	case ActionDiscard:
		return m.discardCard(r)
	case ActionToggleCard:
		for _, msg := range m.messages {
			for _, c := range msg.Cards {
				if c.ID == r.CardID {
					if c.Status == "proposed" && c.Proposal != nil {
						m.overlay = overlayDiff
						m.overlayM = newDiffOverlay(m.styles, c.Proposal, min(max(m.width-10, 60), 100))
						return m
					}
					c.Expanded = !c.Expanded
				}
			}
		}
		m.invalidateMessageCaches()
		m.renderMessages()
		return m
	case ActionToggleCode:
		return m.toggleConcealedAt(r.Target)
	case ActionToggleThink:
		return m.toggleThinkingAt(r.Index)
	case ActionAgentDetail:
		if r.Index >= 0 && r.Index < len(m.sidebar.agentKeys) {
			role := m.sidebar.agentKeys[r.Index]
			row := m.sidebar.agents[role]
			m.overlay = overlayAgentDetail
			m.overlayM = &agentDetailOverlay{Role: role, Status: row.Status, Detail: row.Detail}
		}
		return m
	case ActionCoachOpen:
		if offer := m.visibleCoachOffer(); offer != nil {
			m.overlay = overlayCoach
			m.overlayM = newCoachOverlay(*offer)
		}
		return m
	case ActionToggleHITL:
		m.toggleHITLAt(r.Index)
		if m.activeTab == TabIDE && m.ide != nil {
			m.ide.Focus = ideHITL
		}
	case ActionResize:
		m.beginResize(r.ResizeTarget, r.X, r.Y)
		return m
	case ActionCancelAgent:
		m.orch.CancelRun()
		m.busy = false
		m.finalizeLastAssistantWithToolStatus("error")
		m.appendSystem("cancelled " + r.Label)
		return m
	case ActionFocusNext:
		focusCount := 2
		if m.showActivityRail() {
			focusCount = 3
		}
		m.focus = FocusTarget((int(m.focus) + 1) % focusCount)
		return m
	case ActionToggleActivity:
		m.toggleActivity()
		return m
	case ActionSlashComplete:
		m.completeSlash(r.Target)
		return m
	}
	return m
}

func (m *Model) registerDiffOverlayRegions(diff *diffOverlay, content, box string) {
	if diff == nil || diff.prop == nil {
		return
	}
	boxX := max((m.width-lipgloss.Width(box))/2, 0)
	boxY := max((m.bodyHeight()-lipgloss.Height(box))/2, 0)
	// Dialog uses a one-cell border and two/one cells of horizontal/vertical
	// padding. The footer is the final content row.
	contentX := boxX + 3
	footerY := tabBarRows + boxY + 2 + max(lipgloss.Height(content)-1, 0)
	cardID := ""
	for _, card := range m.pending {
		if card.Proposal != nil && card.Proposal.ID == diff.prop.ID {
			cardID = card.ID
			break
		}
	}
	add := func(label string, action ActionID) {
		idx := strings.Index(diffOverlayFooter, label)
		if idx < 0 {
			return
		}
		x := contentX + lipgloss.Width(diffOverlayFooter[:idx])
		m.regions = append(m.regions, Region{
			X: x, Y: footerY, W: lipgloss.Width(label), H: 1,
			Action: action, CardID: cardID, Label: label, Binding: "click",
		})
	}
	add("→ IDE", ActionOpenProposalIDE)
	add("a accept", ActionAccept)
	add("d decline", ActionDiscard)
}

func (m *Model) updateDiffOverlayMouse(diff *diffOverlay, msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		diff.scrollBy(-3)
		return m, nil
	case tea.MouseButtonWheelDown:
		diff.scrollBy(3)
		return m, nil
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			return m, nil
		}
		for _, region := range m.regions {
			if !region.contains(msg.X, msg.Y) {
				continue
			}
			switch region.Action {
			case ActionOpenProposalIDE, ActionAccept, ActionDiscard:
				m.overlay = overlayNone
				return m.dispatchRegionWithCmd(region)
			}
		}
		return m, nil
	default:
		m.hoverMsg = ""
		for _, region := range m.regions {
			if region.contains(msg.X, msg.Y) {
				m.hoverMsg = region.Label + " — " + region.Binding
				break
			}
		}
		return m, nil
	}
}

func (m *Model) openWorkspaceLocation(target string, line, column int, manual bool) {
	if m.orch == nil {
		return
	}
	root, err := filepath.Abs(m.orch.WorkDirDisplay())
	if err != nil {
		return
	}
	target = strings.TrimSpace(target)
	full := target
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, filepath.FromSlash(target))
	}
	full, err = filepath.Abs(full)
	if err != nil {
		return
	}
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		m.status.pushToast("error", "file is outside the workspace", 3*time.Second)
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		m.status.pushToast("error", "file not found: "+target, 3*time.Second)
		return
	}
	m.switchTab(TabIDE)
	if m.ide != nil {
		if !m.ide.OpenFileAt(filepath.ToSlash(rel)) {
			return
		}
		m.ide.proposalPreview = nil
		if buf := m.ide.Ed.Buffer(); buf != nil && line > 0 {
			buf.Cur.Line = clamp(line-1, 0, max(len(buf.Lines)-1, 0))
			buf.Cur.Col = clamp(max(column-1, 0), 0, len([]rune(buf.LineText(buf.Cur.Line))))
			m.ide.UI.SetScroll(max(buf.Cur.Line-max(m.ide.UI.Height/3, 1), 0))
		}
		if manual {
			m.followAgent = false
		}
	}
}

// focusIDEPaneAt routes a plain click on empty IDE space to the pane under
// the cursor.
func (m *Model) focusIDEPaneAt(x, y int) {
	if m.activeTab != TabIDE || m.ide == nil {
		return
	}
	editorW, treeW, _ := m.idePaneWidths()
	switch {
	case x < treeW:
		m.ide.Focus = ideTree
	case x < treeW+editorW:
		if y >= m.ideCodeTop() {
			m.ide.Focus = ideEditor
		}
	case x >= treeW+editorW:
		m.ide.Focus = ideHITL
	}
}

func (m *Model) beginResize(target, x, y int) {
	if m.activeTab != TabIDE || m.ide == nil {
		return
	}
	now := time.Now()
	if m.resizeTarget == target && now.Sub(m.lastResizeAt) < 400*time.Millisecond {
		m.ideTreePct = 13
		m.ideRailPct = 20
		m.resizing = false
		m.layout()
		m.lastResizeAt = time.Time{}
		return
	}
	m.resizing = true
	m.resizeTarget = target
	m.resizeStartX = x
	m.resizeStartY = y
	m.resizeTreePct = m.ideTreePct
	m.resizeRailPct = m.ideRailPct
	m.lastResizeAt = now
}

func (m *Model) resizeIDEAt(x, y int) {
	if !m.resizing || m.width <= 0 {
		return
	}
	switch m.resizeTarget {
	case 1:
		delta := (x - m.resizeStartX) * 100 / max(m.width, 1)
		m.ideTreePct = clamp(m.resizeTreePct+delta, 9, 24)
	case 2:
		delta := (m.resizeStartX - x) * 100 / max(m.width, 1)
		m.ideRailPct = clamp(m.resizeRailPct+delta, 14, 30)
	}
	m.layout()
}

func (m *Model) handleIDEWheel(x, y, delta int) {
	editorW, treeW, _ := m.idePaneWidths()
	if x >= treeW && x < treeW+editorW {
		if m.ide.proposalPreview != nil {
			m.ide.scrollProposal(delta)
		} else if m.ide.preview {
			m.ide.scrollPreview(delta)
		} else {
			m.ide.UI.Scroll(delta)
		}
		return
	}
	if x < treeW {
		entries := m.ide.treeEntries()
		m.ide.treeSel = clamp(m.ide.treeSel+delta, 0, max(len(entries)-1, 0))
		return
	}
	m.sidebar.moveSelection(delta)
}

func clamp(v, low, high int) int {
	if v < low {
		return low
	}
	if v > high {
		return high
	}
	return v
}

// registerIDERegions maps panel clicks and split borders to semantic actions.
func (m *Model) registerIDERegions(editorW, treeW, railW int) {
	if m.ide == nil {
		return
	}
	bodyHeight := m.bodyHeight()
	m.regions = append(m.regions,
		Region{X: max(treeW-1, 0), Y: tabBarRows, W: 1, H: max(bodyHeight, 1), Action: ActionResize, ResizeTarget: 1, Label: "resize files/editor", Binding: "drag"},
	)
	if railW > 0 {
		m.regions = append(m.regions, Region{X: max(treeW+editorW-1, 0), Y: tabBarRows, W: 1, H: max(bodyHeight, 1), Action: ActionResize, ResizeTarget: 2, Label: "resize editor/agent", Binding: "alt+←/→"})
	}

	// Buffer tabs occupy the first editor row and keep every open file one
	// click away. Their geometry mirrors renderBufferTabs exactly.
	bufferX := treeW
	for i, buf := range m.ide.Ed.Buffers {
		name := bufferTabName(buf.Path)
		if buf.Dirty {
			name += " ●"
		}
		cellW := lipgloss.Width(lipgloss.NewStyle().Padding(0, 1).Render(truncateRunes(name, 24)))
		if bufferX+cellW > treeW+editorW-1 {
			break
		}
		m.regions = append(m.regions, Region{
			X: bufferX, Y: tabBarRows, W: cellW, H: 1,
			Action: ActionSwitchBuffer, Index: i,
			Label: "switch to " + bufferTabName(buf.Path), Binding: "click",
		})
		bufferX += cellW + 1
	}

	treeX := 1
	treeY := tabBarRows + 2
	m.regions = append(m.regions,
		Region{X: 2, Y: tabBarRows, W: 5, H: 1, Action: ActionSwitchExplorer, Target: "files", Label: "show files", Binding: "click"},
		Region{X: 9, Y: tabBarRows, W: max(treeW-10, 1), H: 1, Action: ActionSwitchExplorer, Target: "changes", Label: "show changes", Binding: "click"},
	)
	if m.ide.explorerView == ideExplorerChanges {
		for i, file := range m.sidebar.modFiles {
			y := treeY + i
			if y >= tabBarRows+bodyHeight-1 {
				break
			}
			m.regions = append(m.regions, Region{X: treeX, Y: y, W: max(treeW-2, 1), H: 1, Action: ActionOpenChanged, Index: i, Label: "open " + file.Path, Binding: "click"})
		}
	} else {
		entries := m.ide.treeEntries()
		treeRows := max(bodyHeight-3, 1)
		top := 0
		if m.ide.treeSel >= treeRows {
			top = m.ide.treeSel - treeRows + 1
		}
		for i := top; i < len(entries) && i < top+treeRows; i++ {
			entry := entries[i]
			y := treeY + i - top
			if y >= tabBarRows+bodyHeight-1 {
				break
			}
			label := "open " + entry.Path
			if entry.Dir {
				label = "expand " + entry.Path
			}
			m.regions = append(m.regions, Region{X: treeX, Y: y, W: max(treeW-2, 1), H: 1, Action: ActionOpenFile, Index: i, Label: label, Binding: "enter"})
		}
	}

	hitlX := treeW + editorW + 1
	hitlY := tabBarRows + 2
	if railW > 0 {
		for i := range m.sidebar.hitl {
			y := hitlY + i
			if y >= tabBarRows+bodyHeight-1 {
				break
			}
			m.regions = append(m.regions, Region{X: hitlX, Y: y, W: min(max(railW-2, 1), 4), H: 1, Action: ActionToggleHITL, Index: i, Label: "toggle human action", Binding: "space"})
			if raw := workspaceFilePattern.FindString(m.sidebar.hitl[i].Item); raw != "" {
				path, line, column := parseWorkspaceLocation(raw)
				m.regions = append(m.regions, Region{X: hitlX + 4, Y: y, W: max(railW-6, 1), H: 1, Action: ActionOpenPath, Target: path, Line: line, Column: column, Label: "open task location " + raw, Binding: "click"})
			} else {
				m.regions[len(m.regions)-1].W = max(railW-2, 1)
			}
		}
		if len(m.pending) > 0 {
			card := m.pendingDecisionCard()
			if card == nil {
				card = m.pending[len(m.pending)-1]
			}
			footerY := tabBarRows + bodyHeight - 1
			half := max((railW-2)/2, 1)
			m.regions = append(m.regions,
				Region{X: hitlX, Y: footerY, W: half, H: 1, Action: ActionAccept, CardID: card.ID, Label: "accept proposal", Binding: "a"},
				Region{X: hitlX + half, Y: footerY, W: max(railW-2-half, 1), H: 1, Action: ActionDiscard, CardID: card.ID, Label: "decline proposal", Binding: "d"},
			)
			atomic := proposalRequiresAtomicDecision(card.Proposal)
			if !atomic {
				m.regions = append(m.regions,
					Region{X: hitlX, Y: footerY - 1, W: half, H: 1, Action: ActionAcceptHunk, CardID: card.ID, Label: "accept selected hunk", Binding: "click"},
					Region{X: hitlX + half, Y: footerY - 1, W: max(railW-2-half, 1), H: 1, Action: ActionDiscardHunk, CardID: card.ID, Label: "decline selected hunk", Binding: "click"},
				)
			}
			nav := ansi.Strip(m.renderIDEReviewNavigation(max(railW-4, 1), card))
			arrows := make([]int, 0, 4)
			for index, r := range []rune(nav) {
				if r == '‹' || r == '›' {
					arrows = append(arrows, index)
				}
			}
			navY := footerY - 2
			if atomic && len(arrows) == 2 {
				m.regions = append(m.regions,
					Region{X: hitlX + 2 + arrows[0], Y: navY, W: 1, H: 1, Action: ActionPrevProposal, Label: "previous proposal", Binding: "{"},
					Region{X: hitlX + 2 + arrows[1], Y: navY, W: 1, H: 1, Action: ActionNextProposal, Label: "next proposal", Binding: "}"},
				)
			} else if len(arrows) == 2 {
				m.regions = append(m.regions,
					Region{X: hitlX + 2 + arrows[0], Y: navY, W: 1, H: 1, Action: ActionPrevHunk, Label: "previous hunk", Binding: "["},
					Region{X: hitlX + 2 + arrows[1], Y: navY, W: 1, H: 1, Action: ActionNextHunk, Label: "next hunk", Binding: "]"},
				)
			} else if len(arrows) >= 4 {
				m.regions = append(m.regions,
					Region{X: hitlX + 2 + arrows[0], Y: navY, W: 1, H: 1, Action: ActionPrevProposal, Label: "previous proposal", Binding: "{"},
					Region{X: hitlX + 2 + arrows[1], Y: navY, W: 1, H: 1, Action: ActionNextProposal, Label: "next proposal", Binding: "}"},
					Region{X: hitlX + 2 + arrows[2], Y: navY, W: 1, H: 1, Action: ActionPrevHunk, Label: "previous hunk", Binding: "["},
					Region{X: hitlX + 2 + arrows[3], Y: navY, W: 1, H: 1, Action: ActionNextHunk, Label: "next hunk", Binding: "]"},
				)
			}
		}
	}

	if m.ide.Focus == ideChat || m.input.Value() != "" || m.ide.Ed.HasSelection() {
		chatY := max(tabBarRows+bodyHeight-m.inputH-3, tabBarRows+1)
		m.regions = append(m.regions, Region{X: treeW + 1, Y: chatY, W: max(editorW-2, 1), H: m.inputH + 3, Action: ActionFocusIDEChat, Label: "focus chat", Binding: "ctrl+e"})
	}
}

func (m *Model) acceptCard(r Region) tea.Model {
	for _, msg := range m.messages {
		for _, c := range msg.Cards {
			if c.ID == r.CardID && c.Status == "proposed" {
				m.acceptProposalCard(c)
				if c.Status == "error" {
					m.status.pushToast("error", c.Detail, 4*time.Second)
					m.renderMessages()
					return m
				}
				m.removePending(c)
				m.completeProposalReviewIfSettled()
				m.renderMessages()
				return m
			}
		}
	}
	return m
}

func (m *Model) acceptProposalCard(c *Card) {
	if c.Proposal == nil || m.proposals == nil {
		c.Status = "error"
		c.Detail = "proposal unavailable"
		return
	}
	var err error
	if proposalRequiresAtomicDecision(c.Proposal) {
		err = m.proposals.AcceptVerified(*c.Proposal, projectprofile.ValidateManagedManifest)
	} else {
		err = m.proposals.Accept(*c.Proposal)
	}
	if err != nil {
		c.Status = "error"
		c.Detail = err.Error()
		return
	}
	if c.Lifecycle == "docs" {
		if err := m.orch.CompleteDocs(m.ctx(), c.Proposal.Path); err != nil {
			c.Status = "error"
			c.Detail = err.Error()
			return
		}
		if output := m.drainCommandOutput(); output != "" {
			m.appendSystem(output)
		}
	}
	c.Status = "done"
	c.Detail = "accepted"
}

func (m *Model) discardCard(r Region) tea.Model {
	for _, msg := range m.messages {
		for _, c := range msg.Cards {
			if c.ID == r.CardID && c.Status == "proposed" {
				m.proposals.Discard(*c.Proposal)
				c.Status = "discarded"
				m.removePending(c)
				m.completeProposalReviewIfSettled()
				m.renderMessages()
				return m
			}
		}
	}
	return m
}
