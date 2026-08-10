package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

const maxSlashPreviewRows = 6

func (m *Model) slashMatches() []slashSuggestion {
	if m.activeTab != TabHarness || m.focus != FocusInput {
		return nil
	}
	return matchingSlashSuggestions(m.input.Value())
}

func (m *Model) slashPreviewHeight() int {
	matches := m.slashMatches()
	if len(matches) == 0 {
		return 0
	}
	return min(len(matches), maxSlashPreviewRows) + 1 // items + hint
}

func (m *Model) syncSlashPreview() {
	matches := m.slashMatches()
	if len(matches) == 0 {
		m.slashSelected = 0
	} else {
		m.slashSelected = clamp(m.slashSelected, 0, len(matches)-1)
	}
	m.layout()
}

func (m *Model) moveSlashSelection(delta int) {
	matches := m.slashMatches()
	limit := len(matches)
	if limit == 0 {
		return
	}
	m.slashSelected = (m.slashSelected + delta + limit) % limit
}

func (m *Model) completeSlash(command string) {
	if command == "" {
		return
	}
	m.input.Set(command + " ")
	m.slashSelected = 0
	m.focus = FocusInput
	m.syncSlashPreview()
}

func (m *Model) selectedSlashCommand() string {
	matches := m.slashMatches()
	limit := len(matches)
	if limit == 0 {
		return ""
	}
	m.slashSelected = clamp(m.slashSelected, 0, limit-1)
	return matches[m.slashSelected].Command
}

func (m *Model) updateSlashPreviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if len(m.slashMatches()) == 0 {
		return m, nil, false
	}
	switch msg.Type {
	case tea.KeyUp:
		m.moveSlashSelection(-1)
		return m, nil, true
	case tea.KeyDown:
		m.moveSlashSelection(1)
		return m, nil, true
	case tea.KeyTab:
		m.completeSlash(m.selectedSlashCommand())
		return m, nil, true
	case tea.KeyEnter:
		selected := m.selectedSlashCommand()
		value := strings.TrimSpace(m.input.Value())
		canonical := "/" + canonicalSlashCommand(value)
		if value != selected && canonical != selected {
			m.completeSlash(selected)
			return m, nil, true
		}
	}
	return m, nil, false
}

func (m *Model) renderSlashPreview(width, screenY int) string {
	matches := m.slashMatches()
	limit := min(len(matches), maxSlashPreviewRows)
	if limit == 0 || width < 12 {
		return ""
	}
	inner := max(width-2, 10)
	commandW := min(16, max(inner/3, 10))
	start := 0
	if m.slashSelected >= limit {
		start = m.slashSelected - limit + 1
	}
	start = clamp(start, 0, max(len(matches)-limit, 0))
	var b strings.Builder
	for row := 0; row < limit; row++ {
		index := start + row
		suggestion := matches[index]
		marker := "  "
		rowStyle := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenSmoke))
		commandStyle := lipgloss.NewStyle().Foreground(m.styles.T.Color(TokenDolly))
		if index == m.slashSelected {
			marker = "▸ "
			rowStyle = rowStyle.Background(m.styles.T.Color(TokenPanel))
			commandStyle = commandStyle.Bold(true)
		}
		command := commandStyle.Render(suggestion.Command)
		padding := strings.Repeat(" ", max(commandW-lipgloss.Width(suggestion.Command), 1))
		descW := max(inner-2-commandW, 4)
		description := m.styles.Hint.Render(truncateRunes(suggestion.Description, descW))
		line := marker + command + padding + description
		b.WriteString(rowStyle.Width(inner).MaxWidth(inner).Render(clampANSIWidth(line, inner)))
		b.WriteString("\n")
		m.regions = append(m.regions, Region{
			X: 0, Y: screenY + row, W: width, H: 1,
			Action: ActionSlashComplete, Target: suggestion.Command,
			Label: "complete " + suggestion.Command, Binding: "click or tab",
		})
	}
	position := fmt.Sprintf("%d/%d", m.slashSelected+1, len(matches))
	hint := m.styles.InputHint.Render("  " + position + " · ↑/↓ select · tab complete · click command")
	b.WriteString(clampANSIWidth(hint, inner))
	return b.String()
}
