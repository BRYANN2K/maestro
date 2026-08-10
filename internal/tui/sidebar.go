package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/orchestrator"
)

// Icons are Nerd Font glyphs with ASCII fallback.
const (
	iconAgent   = "◉"
	iconAgentID = "·"
	iconDone    = "✓"
	iconErr     = "✗"
	iconMCPOn   = "●"
	iconMCPOff  = "○"
	iconTodo    = "☐"
	iconDone2   = "☑"
)

// Sidebar is the on-demand activity panel: context, sub-agents, MCP, HITL,
// and the session's changed files (+N/−N diff chips).
type Sidebar struct {
	T          Theme
	width      int
	height     int
	agents     map[string]agentRow
	agentKeys  []string // sorted keys for stable rendering
	hitl       []agentcore.HITLItem
	checked    map[string]bool
	selected   int
	scroll     int
	queue      int // pending proposal count (pill)
	hitlScroll scrollbar
	modFiles   []git.NumStat
	hitlRows   map[int]int // rendered row, relative to the rail body
	agentRows  map[int]int // rendered row, relative to the rail body
	fileRows   map[int]int // changed-file row, relative to the rail body
	coach      *coachOffer
	coachRow   int // offer row, relative to the rail body; -1 when hidden
}

type agentRow struct {
	Role    string
	Status  string
	Detail  string
	Updated time.Time
}

// setFiles replaces the changed-files stats (end of turn refresh).
func (s *Sidebar) setFiles(stats []git.NumStat) {
	s.modFiles = stats
}

// NewSidebar builds the sidebar state.
func NewSidebar(t Theme) *Sidebar {
	return &Sidebar{
		T:       t,
		agents:  map[string]agentRow{},
		checked: map[string]bool{},
	}
}

// setAgent updates a sub-agent row from an event.
func (s *Sidebar) setAgent(sa agentcore.SubAgentStatus) {
	s.agents[sa.Role] = agentRow{Role: sa.Role, Status: sa.Status, Detail: sa.Detail, Updated: time.Now()}
	s.agentKeys = make([]string, 0, len(s.agents))
	for k := range s.agents {
		s.agentKeys = append(s.agentKeys, k)
	}
	sort.Strings(s.agentKeys)
}

// setItem records a HITL item.
func (s *Sidebar) setItem(it agentcore.HITLItem) {
	// `hitl` was a legacy synthetic summary row. It is not a human action and
	// must never consume a checkbox, progress slot, or keyboard selection.
	if it.ID == "hitl" {
		return
	}
	if it.Status == "done" {
		s.checked[it.ID] = true
		return
	}
	s.checked[it.ID] = false
	for i := range s.hitl {
		if s.hitl[i].ID == it.ID {
			s.hitl[i] = it
			return
		}
	}
	s.hitl = append(s.hitl, it)
}

// refresh reloads HITL items from the orchestrator.
func (s *Sidebar) refresh(orch *orchestrator.Orchestrator) {
	items, err := orch.HITLItems(context.Background())
	if err == nil {
		s.hitl = s.hitl[:0]
		s.checked = map[string]bool{}
		for _, item := range items {
			if item.ID == "hitl" {
				continue
			}
			s.hitl = append(s.hitl, item)
			if item.Status == "done" {
				s.checked[item.ID] = true
			}
		}
		if len(s.hitl) == 0 {
			s.selected, s.scroll = 0, 0
		} else if s.selected >= len(s.hitl) {
			s.selected = len(s.hitl) - 1
		}
	}
}

// moveSelection moves the HITL selection (keyboard).
func (s *Sidebar) moveSelection(delta int) {
	if len(s.hitl) == 0 {
		return
	}
	s.selected += delta
	if s.selected < 0 {
		s.selected = 0
	}
	if s.selected >= len(s.hitl) {
		s.selected = len(s.hitl) - 1
	}
	rows := max(s.height-4, 3)
	if s.selected >= s.scroll+rows {
		s.scroll = s.selected - rows + 1
	}
	if s.selected < s.scroll {
		s.scroll = s.selected
	}
}

// toggleSelected toggles the currently highlighted HITL checkbox.
func (s *Sidebar) toggleSelected() (id string, checked bool, ok bool) {
	if s.selected < 0 || s.selected >= len(s.hitl) {
		return "", false, false
	}
	it := s.hitl[s.selected]
	s.checked[it.ID] = !s.checked[it.ID]
	return it.ID, s.checked[it.ID], true
}

// toggleAt toggles the checkbox at index i (mouse).
func (s *Sidebar) toggleAt(i int) (id string, checked bool, ok bool) {
	if i < 0 || i >= len(s.hitl) {
		return "", false, false
	}
	it := s.hitl[i]
	s.checked[it.ID] = !s.checked[it.ID]
	return it.ID, s.checked[it.ID], true
}

// complete marks a deterministic human action as resolved. Proposal review
// uses this after the user accepts or discards the last staged write.
func (s *Sidebar) complete(id string) {
	if id == "" {
		return
	}
	s.checked[id] = true
}

func (s *Sidebar) reopen(id string) {
	if id == "" {
		return
	}
	s.checked[id] = false
}

// allChecked reports whether every pending item is checked.
func (s *Sidebar) allChecked() bool {
	if len(s.hitl) == 0 {
		return true
	}
	for _, it := range s.hitl {
		if !s.checked[it.ID] {
			return false
		}
	}
	return true
}

func (s *Sidebar) hitlProgress() (completed, total, pendingBlocking int) {
	for _, item := range s.hitl {
		total++
		if s.checked[item.ID] {
			completed++
		} else if item.Blocking {
			pendingBlocking++
		}
	}
	return completed, total, pendingBlocking
}

// View renders the operational rail. It is intentionally compact: this is a
// control surface, not a second dashboard competing with the conversation.
func (s *Sidebar) View(styles Styles, orch *orchestrator.Orchestrator) string {
	if s.width <= 0 {
		s.width = 20
	}
	var b strings.Builder
	s.hitlRows = map[int]int{}
	s.agentRows = map[int]int{}
	s.fileRows = map[int]int{}
	s.coachRow = -1

	completed, total, pendingBlocking := s.hitlProgress()
	section := func(title string) {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(styles.SidebarSection.Render("▎ "+title) + "\n")
	}

	section(fmt.Sprintf("HUMAN ACTIONS  %d/%d", completed, total))
	if total == 0 {
		b.WriteString(styles.SidebarChecked.Render("✓ No approvals required") + "\n")
	} else {
		rows := max(min(s.height/3, 7), 3)
		if s.selected >= s.scroll+rows {
			s.scroll = s.selected - rows + 1
		}
		if s.selected < s.scroll {
			s.scroll = s.selected
		}
		for i := s.scroll; i < len(s.hitl) && i < s.scroll+rows; i++ {
			it := s.hitl[i]
			mark := "[ ]"
			if s.checked[it.ID] {
				mark = "[x]"
			}
			line := fmt.Sprintf("%s %s", mark, safeIDEPlainText(it.Item))
			var st lipgloss.Style
			switch {
			case i == s.selected && !s.checked[it.ID]:
				st = styles.SidebarActive
			case s.checked[it.ID]:
				st = styles.SidebarChecked
			default:
				st = styles.SidebarItem
			}
			s.hitlRows[i] = lipgloss.Height(strings.TrimSuffix(b.String(), "\n"))
			b.WriteString(st.Width(max(s.width-2, 1)).Render(truncateRunes(line, max(s.width-3, 1))) + "\n")
		}
	}

	incomplete := total - completed
	if pendingBlocking > 0 {
		b.WriteString(styles.SidebarItem.Foreground(styles.T.Color(TokenSash)).Bold(true).Render(fmt.Sprintf("● PAUSED · %d blocking", pendingBlocking)) + "\n")
	} else if incomplete > 0 {
		b.WriteString(styles.SidebarItem.Foreground(styles.T.Color(TokenMustard)).Bold(true).Render(fmt.Sprintf("● ACTIONS · %d to review", incomplete)) + "\n")
	} else {
		b.WriteString(styles.SidebarItem.Foreground(styles.T.Color(TokenJulep)).Bold(true).Render("● READY · actions complete") + "\n")
	}

	section("RUNS · CURRENT")
	if len(s.agents) == 0 {
		b.WriteString(styles.SidebarItem.Render("○ planner  idle") + "\n")
	} else {
		for i, role := range s.agentKeys {
			row := s.agents[role]
			color, mark := s.T.Color(TokenSmoke), "○"
			switch row.Status {
			case "running":
				color, mark = s.T.Color(TokenMustard), "◉"
			case "done":
				color, mark = s.T.Color(TokenJulep), "✓"
			case "error":
				color, mark = s.T.Color(TokenSash), "×"
			}
			stamp := ""
			if !row.Updated.IsZero() {
				stamp = row.Updated.Format("15:04")
			}
			left := fmt.Sprintf("%s %-9s %s", mark, safeIDEPlainText(role), safeIDEPlainText(row.Status))
			gap := max(s.width-4-len([]rune(left))-len([]rune(stamp)), 1)
			line := left + strings.Repeat(" ", gap) + stamp
			s.agentRows[i] = lipgloss.Height(strings.TrimSuffix(b.String(), "\n"))
			b.WriteString(lipgloss.NewStyle().Foreground(color).Render(truncateRunes(line, max(s.width-1, 1))) + "\n")
		}
	}

	section(fmt.Sprintf("CHANGED  %d", len(s.modFiles)))
	if len(s.modFiles) == 0 {
		b.WriteString(styles.SidebarItem.Render("none · working tree clean") + "\n")
	} else {
		limit := min(len(s.modFiles), max(s.height/5, 3))
		for i, f := range s.modFiles[:limit] {
			s.fileRows[i] = lipgloss.Height(strings.TrimSuffix(b.String(), "\n"))
			b.WriteString(s.renderModifiedFile(styles, f) + "\n")
		}
		if len(s.modFiles) > limit {
			b.WriteString(styles.SidebarItem.Render(fmt.Sprintf("  +%d more", len(s.modFiles)-limit)) + "\n")
		}
	}
	if s.queue > 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenMustard)).Render(fmt.Sprintf("%d proposal(s) queued", s.queue)) + "\n")
	}
	if s.coach != nil {
		duration := safeIDEPlainText(s.coach.Duration)
		if duration == "" {
			duration = "2 min"
		}
		section("COACH · " + strings.ToUpper(duration))
		s.coachRow = lipgloss.Height(strings.TrimSuffix(b.String(), "\n"))
		title := truncateIDEPlainText(s.coach.Title, max(s.width-4, 8))
		b.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenDolly)).Bold(true).Render("∴ "+title) + "\n")
		b.WriteString(styles.SidebarItem.Render("  /learn next · optional") + "\n")
	}

	section("SESSION")
	b.WriteString(styles.SidebarItem.Render(budgetLine(orch)) + "\n")
	if budget := orch.BudgetState(); budget != nil {
		barW := max(s.width-9, 6)
		filled := clamp(int(budget.Percent()*float64(barW)), 0, barW)
		filledBar := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Render(strings.Repeat("━", filled))
		emptyBar := lipgloss.NewStyle().Foreground(styles.T.Color(TokenIron)).Render(strings.Repeat("─", barW-filled))
		pct := fmt.Sprintf("%d%%", int(budget.Percent()*100))
		b.WriteString(filledBar + emptyBar + "  " + styles.SidebarItem.Render(pct) + "\n")
	}

	return b.String()
}

// renderModifiedFile renders one changed file with +N/−N chips
// (opencode-style): green additions, red removals, truncated path.
func (s *Sidebar) renderModifiedFile(styles Styles, f git.NumStat) string {
	var chips []string
	if f.Additions > 0 {
		chips = append(chips, lipgloss.NewStyle().Foreground(s.T.Color(TokenJulep)).Bold(true).Render(fmt.Sprintf("+%d", f.Additions)))
	}
	if f.Removals > 0 {
		chips = append(chips, lipgloss.NewStyle().Foreground(s.T.Color(TokenSash)).Bold(true).Render(fmt.Sprintf("-%d", f.Removals)))
	}
	statsW := 0
	for _, chip := range chips {
		statsW += lipgloss.Width(chip) + 1
	}
	path := truncateIDEPlainText(f.Path, max(s.width-statsW-6, 8))
	line := "  " + path
	if len(chips) > 0 {
		gap := max(s.width-len([]rune(path))-statsW-5, 1)
		line += strings.Repeat(" ", gap) + strings.Join(chips, " ")
	}
	if f.Untracked {
		line += " " + lipgloss.NewStyle().Foreground(s.T.Color(TokenMustard)).Render("new")
	}
	return clampANSIWidth(lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke)).Render(line), s.width)
}

// HITLView renders only the Human Actions section (IDE right panel).
func (s *Sidebar) HITLView(styles Styles) string {
	rows := s.height - 4
	if rows < 3 {
		rows = 3
	}
	s.hitlScroll.set(rows, rows, len(s.hitl), s.scroll)
	var lines []string
	if len(s.hitl) == 0 {
		lines = append(lines, styles.SidebarItem.Render("none"))
	} else {
		for i := s.scroll; i < len(s.hitl) && i < s.scroll+rows; i++ {
			it := s.hitl[i]
			mark := iconTodo
			if s.checked[it.ID] {
				mark = iconDone2
			}
			line := fmt.Sprintf(" %s %s", mark, safeIDEPlainText(it.Item))
			var st lipgloss.Style
			switch {
			case i == s.selected && !s.checked[it.ID]:
				st = styles.SidebarActive
			case s.checked[it.ID]:
				st = styles.SidebarChecked
			default:
				st = styles.SidebarItem
			}
			lines = append(lines, st.Width(s.width-4).Render(line))
		}
		if s.allChecked() {
			lines = append(lines, styles.SidebarItem.Render(iconDone2+" all complete"))
		}
	}
	for len(lines) < rows {
		lines = append(lines, "")
	}
	block := strings.Join(lines, "\n")
	if bar := s.hitlScroll.View(styles); bar != "" {
		block = lipgloss.JoinHorizontal(lipgloss.Top, block, bar)
	}
	return block
}

// budgetLine renders the F2 guardrail state.
func budgetLine(orch *orchestrator.Orchestrator) string {
	b := orch.BudgetState()
	if b == nil {
		return "no limits"
	}
	return b.String()
}
