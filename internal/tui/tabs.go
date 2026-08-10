package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/session"
)

// Tab is a top-level workspace in the TUI.
type Tab int

const (
	TabHarness Tab = iota
	TabIDE
)

const tabBarRows = 1

func (t Tab) String() string {
	if t == TabIDE {
		return "IDE"
	}
	return "Maestro"
}

// ActiveTab exposes the selected workspace for headless drivers and tests.
func (m *Model) ActiveTab() Tab { return m.activeTab }

// SwitchTab selects a workspace without destroying the other workspace's state.
func (m *Model) SwitchTab(tab Tab) { m.switchTab(tab) }

// ToggleIDE preserves the original /ide and :q behavior while the UI uses
// explicit tabs as its source of truth.
func (m *Model) ToggleIDE() {
	if m.activeTab == TabIDE {
		m.closeIDE()
		return
	}
	m.switchTab(TabIDE)
}

func (m *Model) switchTab(tab Tab) {
	if tab != TabHarness && tab != TabIDE {
		return
	}
	if tab == TabIDE {
		created := m.ide == nil
		if created {
			project := m.orch.WorkDirDisplay()
			m.ide = NewIDE(m, project, git.New(project))
		}
		m.ensureIDEProportions()
		m.activeTab = TabIDE
		m.layout()
		if created {
			m.status.pushToast("info", "IDE tab — /ide or :q to leave", 3*time.Second)
		}
	} else {
		m.activeTab = TabHarness
		m.resizing = false
		m.layout()
	}
	m.renderMessages()
	if m.activeTab == TabHarness && m.followOutput {
		m.viewport.GotoBottom()
	}
}

func (m *Model) closeIDE() {
	if m.ide != nil {
		m.ide.Save()
	}
	m.ide = nil
	m.activeTab = TabHarness
	m.resizing = false
	m.layout()
	m.renderMessages()
	if m.followOutput {
		m.viewport.GotoBottom()
	}
}

func (m *Model) cycleTab() {
	if m.activeTab == TabHarness {
		m.switchTab(TabIDE)
		return
	}
	m.switchTab(TabHarness)
}

// tabForKey uses Alt-runes as the portable workspace shortcuts. Synthetic
// ctrl+N messages remain accepted for terminal adapters that can emit them.
func tabForKey(msg tea.KeyMsg) (Tab, bool) {
	if msg.String() == "ctrl+1" || (msg.Type == tea.KeyRunes && msg.Alt && string(msg.Runes) == "1") {
		return TabHarness, true
	}
	if msg.String() == "ctrl+2" || (msg.Type == tea.KeyRunes && msg.Alt && string(msg.Runes) == "2") {
		return TabIDE, true
	}
	return TabHarness, false
}

// isCtrlTab accepts the modified-tab form emitted by common terminals and
// the friendly string form used by test/headless adapters.
func (m *Model) isCtrlTab(msg tea.KeyMsg) bool {
	return msg.String() == "ctrl+tab" || (msg.Type == tea.KeyTab && msg.Alt)
}

func (m *Model) renderTabBar() string {
	labels := []struct {
		tab     Tab
		label   string
		binding string
	}{
		{TabHarness, "⌥1 AGENT", "alt+1"},
		{TabIDE, "⌥2 IDE", "alt+2"},
	}

	var row string
	x := 0
	for _, item := range labels {
		style := m.styles.TabInactive
		if item.tab == m.activeTab {
			style = m.styles.TabActive
		}
		cell := style.Render(" " + item.label + " ")
		cellW := lipgloss.Width(cell)
		m.regions = append(m.regions, Region{
			X: x, Y: 0, W: cellW, H: tabBarRows,
			Action: ActionSwitchTab, Tab: item.tab,
			Label: item.tab.String(), Binding: item.binding,
		})
		row += cell
		x += cellW
	}
	project := "maestro"
	branch := ""
	if m.orch != nil {
		if name := m.orch.ProjectName(); name != "" {
			project = name
		}
		branch = m.orch.BranchDisplay()
	}
	metaText := "  ◇ " + truncateIDEPlainText(project, 24)
	if m.sessionTitle != "" && m.width >= 92 {
		metaText += " · " + truncateIDEPlainText(m.sessionTitle, 28)
	}
	metaText += "   ⎇ " + truncateIDEPlainText(branch, 28)
	meta := m.styles.TabInactive.Render(metaText)
	if x+lipgloss.Width(meta)+18 < m.width {
		row += meta
		x += lipgloss.Width(meta)
	}

	railColor := m.styles.T.Color(TokenIron)
	if m.activityOpen {
		railColor = m.styles.T.Color(TokenCharple)
	}
	railToggle := lipgloss.NewStyle().Foreground(railColor).Render("◧")
	mark := lipgloss.NewStyle().
		Foreground(m.styles.T.Color(TokenChar)).
		Background(m.styles.T.Color(TokenCharple)).
		Bold(true).
		Padding(0, 1).
		Render("M")
	// The rail toggle, the two gaps around the runtime summary, the padded
	// Maestro mark, and the final safety cell consume nine columns. Reserving
	// only six let a context badge widen the tab bar past the terminal edge;
	// Lipgloss then wrapped the mark onto a second row and the frame-height
	// fitter spliced an orphan ellipsis into the Coach dialog.
	fixedChromeWidth := lipgloss.Width(railToggle) + 2 + 2 + lipgloss.Width(mark) + 1
	right := m.renderRuntimeChrome(max(m.width-x-fixedChromeWidth, 0))
	if right != "" {
		right = railToggle + "  " + right + "  "
	} else {
		right = railToggle + "  "
	}
	right += mark
	// Keep the Maestro mark actionable on unusually dense tab labels even if
	// a future runtime badge grows. At supported terminal widths the toggle and
	// mark always fit; the runtime summary is the optional element.
	available := max(m.width-x-1, 0)
	if lipgloss.Width(right) > available {
		right = railToggle + "  " + mark
	}
	markX := max(m.width-lipgloss.Width(right)-1, x)
	if markX > lipgloss.Width(row) {
		row += strings.Repeat(" ", markX-lipgloss.Width(row))
	}
	m.regions = append(m.regions, Region{
		X: markX, Y: 0, W: lipgloss.Width(railToggle), H: 1,
		Action: ActionToggleActivity, Label: "toggle activity rail", Binding: "ctrl+b",
	})
	row += right
	return m.styles.TabBar.Width(m.width).MaxWidth(m.width).Render(clampANSIWidth(row, m.width))
}

// renderRuntimeChrome exposes the orchestration contract in the command bar.
// Chat is deliberately labelled read-only: the LLM may discuss and inspect,
// but only /propose can create a spec proposal.
func (m *Model) renderRuntimeChrome(width int) string {
	if m.orch == nil || width < 18 {
		return ""
	}
	label := "DISCOVERY · READ ONLY"
	color := m.styles.T.Color(TokenSmoke)
	switch m.orch.Phase() {
	case session.PhasePropose:
		label, color = "PROPOSAL · REVIEW", m.styles.T.Color(TokenMustard)
	case session.PhaseSpec:
		label, color = "SPEC · READY", m.styles.T.Color(TokenJulep)
		if m.pendingHumanActions() > 0 {
			label, color = "SPEC · PAUSED", m.styles.T.Color(TokenSash)
		}
	case session.PhaseBuild:
		label, color = "BUILD · READY", m.styles.T.Color(TokenJulep)
		if m.busy {
			label, color = "BUILD · RUNNING", m.styles.T.Color(TokenCharple)
		} else if m.pendingHumanActions() > 0 {
			label, color = "BUILD · PAUSED", m.styles.T.Color(TokenSash)
		}
	case session.PhaseReview:
		label, color = "REVIEW · CHECKS", m.styles.T.Color(TokenMustard)
	case session.PhaseDocs:
		label, color = "DOCS · WRITING", m.styles.T.Color(TokenCharple)
	case session.PhaseArchive:
		label, color = "ARCHIVE · READY", m.styles.T.Color(TokenJulep)
	}
	if m.busy && m.orch.Phase() == session.PhaseChat {
		label, color = "DISCOVERY · INSPECTING", m.styles.T.Color(TokenCharple)
	}
	phase := lipgloss.NewStyle().
		Foreground(color).
		Background(m.styles.T.Blend(TokenPanel, TokenCharple, 0.08)).
		Bold(true).
		Padding(0, 1).
		Render(label)
	model := truncateRunes(safeIDEPlainText(m.orch.ActiveModel()), 25)
	modelText := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render(model)
	used, total := m.orch.ContextUsage()
	contextText := ""
	if total > 0 {
		contextText = lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke)).Render(fmt.Sprintf("ctx %d%%", min(used*100/total, 100)))
	}
	if lipgloss.Width(phase) > width {
		return ""
	}
	separator := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenIron)).Render("  ·  ")
	parts := []string{phase}
	if model != "" {
		candidate := strings.Join(append(append([]string(nil), parts...), modelText), separator)
		if lipgloss.Width(candidate) <= width {
			parts = append(parts, modelText)
		}
	}
	if contextText != "" {
		candidate := strings.Join(append(append([]string(nil), parts...), contextText), separator)
		if lipgloss.Width(candidate) <= width {
			parts = append(parts, contextText)
		}
	}
	return strings.Join(parts, separator)
}

func (m *Model) pendingHumanActions() int {
	if m.sidebar == nil {
		return 0
	}
	remaining := 0
	for _, item := range m.sidebar.hitl {
		if item.Blocking && !m.sidebar.checked[item.ID] {
			remaining++
		}
	}
	return remaining
}

func (m *Model) ensureIDEProportions() {
	if m.ideTreePct == 0 {
		m.ideTreePct = 13
	}
	if m.ideRailPct == 0 {
		m.ideRailPct = 20
	}
}

// idePaneWidths returns the approved explorer/editor/agent grid. The return
// order stays editor, tree, rail for compatibility with the editor plumbing,
// while renderIDE places the tree first on screen.
func (m *Model) idePaneWidths() (editorW, treeW, hitlW int) {
	m.ensureIDEProportions()
	available := max(m.width, 1)
	// A three-column IDE cannot preserve usable minimums in a very narrow
	// terminal. Collapse the companion rail first and allocate the remaining
	// cells proportionally between explorer and editor. This also guarantees
	// that resize events can never produce negative pane widths/hit regions.
	if available < 68 {
		if available == 1 {
			return 1, 0, 0
		}
		treeW = clamp(available/4, 1, min(12, available-1))
		return available - treeW, treeW, 0
	}
	treePct := clamp(m.ideTreePct, 9, 22)
	railPct := clamp(m.ideRailPct, 14, 30)
	treeW = max(available*treePct/100, 14)
	if m.activeTab == TabIDE && !m.activityOpen {
		return available - treeW, treeW, 0
	}
	hitlW = max(available*railPct/100, 20)
	editorW = available - treeW - hitlW
	if editorW < 40 {
		deficit := 40 - editorW
		if hitlW > 16 {
			shrink := min(deficit, hitlW-16)
			hitlW -= shrink
			deficit -= shrink
		}
		if deficit > 0 && treeW > 12 {
			treeW -= min(deficit, treeW-12)
		}
		editorW = available - treeW - hitlW
	}
	return
}
