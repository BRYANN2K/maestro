package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

// ActionID identifies one user action. One action = one handler, two
// triggers (keyboard + mouse, §5.10).
type ActionID string

// All user actions.
const (
	ActionSend            ActionID = "send_message"
	ActionNewline         ActionID = "newline"
	ActionScrollUp        ActionID = "scroll_up"
	ActionScrollDown      ActionID = "scroll_down"
	ActionFocusNext       ActionID = "focus_next"
	ActionPalette         ActionID = "command_palette"
	ActionModelPicker     ActionID = "model_picker"
	ActionSessionPicker   ActionID = "session_picker"
	ActionCancelTour      ActionID = "cancel_tour"
	ActionQuit            ActionID = "quit"
	ActionKeymap          ActionID = "keymap_viewer"
	ActionToggleCard      ActionID = "toggle_card"
	ActionAccept          ActionID = "accept"
	ActionDiscard         ActionID = "discard"
	ActionToggleHITL      ActionID = "toggle_hitl"
	ActionApprovePerm     ActionID = "approve_permission"
	ActionDenyPerm        ActionID = "deny_permission"
	ActionCancelAgent     ActionID = "cancel_agent"
	ActionSidebarUp       ActionID = "sidebar_up"
	ActionSidebarDown     ActionID = "sidebar_down"
	ActionEscape          ActionID = "escape"
	ActionSwitchTab       ActionID = "switch_tab"
	ActionResize          ActionID = "resize_panel"
	ActionOpenFile        ActionID = "open_file"
	ActionSwitchBuffer    ActionID = "switch_buffer"
	ActionSwitchExplorer  ActionID = "switch_explorer_view"
	ActionOpenChanged     ActionID = "open_changed_file"
	ActionOpenPath        ActionID = "open_workspace_path"
	ActionOpenProposalIDE ActionID = "open_proposal_in_ide"
	ActionPrevProposal    ActionID = "previous_proposal"
	ActionNextProposal    ActionID = "next_proposal"
	ActionPrevHunk        ActionID = "previous_hunk"
	ActionNextHunk        ActionID = "next_hunk"
	ActionAcceptHunk      ActionID = "accept_hunk"
	ActionDiscardHunk     ActionID = "discard_hunk"
	ActionToggleTree      ActionID = "toggle_tree"
	ActionFocusIDEChat    ActionID = "focus_ide_chat"
	ActionToggleCode      ActionID = "toggle_code_block"
	ActionToggleThink     ActionID = "toggle_thinking"
	ActionTimeline        ActionID = "timeline"
	ActionAgentDetail     ActionID = "agent_detail"
	ActionEditExternal    ActionID = "edit_external"
	ActionToggleActivity  ActionID = "toggle_activity"
	ActionSlashComplete   ActionID = "complete_slash_command"
	ActionAddContext      ActionID = "add_context"
	ActionIDESelection    ActionID = "ide_selection_actions"
	ActionAskFix          ActionID = "ask_maestro_to_fix"
	ActionToggleFollow    ActionID = "toggle_follow_agent"
	ActionCoachOpen       ActionID = "open_coach"
)

// Binding is one keyboard trigger for an action.
type Binding struct {
	Key         string // display form, e.g. "ctrl+p"
	Description string
}

// keymap is the built-in binding table (§5.10.1).
var keymap = []struct {
	Action  ActionID
	KeyType tea.KeyType
	KeyStr  string // display
	Alt     bool
	Desc    string
}{
	{ActionSend, tea.KeyEnter, "enter", false, "Send message"},
	{ActionNewline, tea.KeyEnter, "shift+enter", true, "Newline"},
	{ActionScrollUp, tea.KeyPgUp, "pgup", false, "Scroll up"},
	{ActionScrollDown, tea.KeyPgDown, "pgdn", false, "Scroll down"},
	{ActionFocusNext, tea.KeyTab, "tab", false, "Cycle focus"},
	{ActionPalette, tea.KeyCtrlP, "ctrl+p", false, "Command palette"},
	{ActionModelPicker, tea.KeyCtrlL, "ctrl+l", false, "Model picker"},
	{ActionSessionPicker, tea.KeyCtrlR, "ctrl+r", false, "Session picker"},
	{ActionCancelTour, tea.KeyCtrlC, "ctrl+c", false, "Cancel tour / quit (2×)"},
	{ActionQuit, tea.KeyCtrlQ, "ctrl+q", false, "Quit"},
	{ActionKeymap, 0, "space ?", false, "Keymap viewer"},
	{ActionEscape, tea.KeyEsc, "esc esc", false, "Close / cancel active task"},
	{ActionToggleCode, 0, "v", false, "Expand/collapse code block"},
	{ActionToggleThink, 0, "t", false, "Expand/collapse working summary"},
	{ActionTimeline, tea.KeyCtrlT, "ctrl+t", false, "Message timeline"},
	{ActionEditExternal, tea.KeyCtrlE, "ctrl+e", false, "Edit message in $EDITOR"},
	{ActionToggleActivity, tea.KeyCtrlB, "ctrl+b", false, "Toggle activity panel"},
}

// KeyFor returns the display string of the first binding of an action.
func KeyFor(a ActionID) string {
	for _, b := range keymap {
		if b.Action == a {
			if b.KeyStr != "" {
				return b.KeyStr
			}
			return b.KeyStr
		}
	}
	return "?"
}

// ActionFor maps a key message to an action.
func ActionFor(msg tea.KeyMsg) (ActionID, bool) {
	if msg.Type == tea.KeyEnter && msg.Alt {
		return ActionNewline, true
	}
	if msg.Type == tea.KeyRunes {
		if msg.String() == "?" && msg.Alt {
			return ActionKeymap, true
		}
		return "", false
	}
	for _, b := range keymap {
		if b.KeyType == msg.Type {
			return b.Action, true
		}
	}
	return "", false
}

// keyRow is one help entry.
type keyRow struct {
	key  string
	desc string
}

// keymapRows flattens the binding table into help rows, deduped by key
// (reverse-scan: the first binding of a key wins, position preserved).
func keymapRows() []keyRow {
	seen := map[string]bool{}
	var rows []keyRow
	for _, e := range keymap {
		key := e.KeyStr
		if key == "" {
			key = tea.KeyMsg{Type: e.KeyType}.String()
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, keyRow{key, e.Desc})
	}
	return rows
}

// renderHelpColumns lays the rows out in columns of helpRowsPerCol, dropped
// greedily against the width budget, with the lipgloss #209 ragged-height
// workaround (last column equalized to the first via Place + whitespace
// background) before the horizontal join.
func renderHelpColumns(styles Styles, width int, rows []keyRow) string {
	const helpRowsPerCol = 10
	// Defensive dedup (reverse-scan): the first binding of a key wins.
	seen := map[string]bool{}
	var dedup []keyRow
	for _, r := range rows {
		if seen[r.key] {
			continue
		}
		seen[r.key] = true
		dedup = append(dedup, r)
	}
	rows = dedup
	var cols []string
	for i := 0; i < len(rows); i += helpRowsPerCol {
		end := min(i+helpRowsPerCol, len(rows))
		var b strings.Builder
		for _, r := range rows[i:end] {
			pad := max(18-lipgloss.Width(r.key), 1)
			b.WriteString("  " + r.key + strings.Repeat(" ", pad) + r.desc + "\n")
		}
		cols = append(cols, strings.TrimSuffix(b.String(), "\n"))
	}
	// Width budget: drop whole columns once the accumulated width (with the
	// 3-cell margin) would overflow the box.
	var kept []string
	for _, c := range cols {
		if lipgloss.Width(c) > width-2 {
			break
		}
		if len(kept) > 0 && lipgloss.Width(kept[0])+3+lipgloss.Width(c) > width-2 {
			break
		}
		kept = append(kept, c)
	}
	// Narrow terminal: a single column keeps every row (no truncation), the
	// multi-column layout only kicks in when two columns fit.
	if len(kept) <= 1 {
		return strings.Join(cols, "\n")
	}
	if len(kept) > 1 {
		target := lipgloss.Height(kept[0])
		if h := lipgloss.Height(kept[len(kept)-1]); h < target {
			kept[len(kept)-1] = lipgloss.Place(
				lipgloss.Width(kept[len(kept)-1]), target,
				lipgloss.Left, lipgloss.Top, kept[len(kept)-1],
				lipgloss.WithWhitespaceStyle(lipgloss.NewStyle().Background(styles.T.Color(TokenSurface))),
			)
		}
	}
	joined := kept[0]
	for _, c := range kept[1:] {
		spacer := lipgloss.NewStyle().Width(3).Height(lipgloss.Height(joined)).Render(" ")
		joined = lipgloss.JoinHorizontal(lipgloss.Top, joined, spacer, c)
	}
	return joined
}

// KeymapView renders the effective binding table for `Space ?`.
func KeymapView(styles Styles, width int) string {
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("Keymap") + "\n\n")
	if cols := renderHelpColumns(styles, width, keymapRows()); cols != "" {
		b.WriteString(cols + "\n")
	}
	b.WriteString("\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke)).Render("Space ? to close — hover shows keybindings on clickable elements"))
	b.WriteString("\n\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("Workspace"))
	b.WriteString("\n  alt+1           Maestro\n  alt+2           IDE\n  ctrl+tab        Next tab")
	b.WriteString("\n\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("IDE"))
	b.WriteString("\n  standard        direct typing (default)\n  settings        Vim is optional\n  shift+arrows    select + type to replace\n  drag editor     select + edit/ask/comment\n  Space p         Markdown preview\n  ctrl+w h/j/k/l  Navigate panels\n  Space a         Ask about selection / actions\n  Space t         Theme browser")
	b.WriteString("\n\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true).Render("Mouse"))
	b.WriteString("\n  wheel           Scroll focused pane\n  drag divider    Resize panes\n  drag editor     Select code")
	return b.String()
}
