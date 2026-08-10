package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

// authOverlay collects a provider credential without ever rendering the
// secret itself or sending it through the chat/input history.
type authOverlay struct {
	provider string
	model    string
	role     string
	key      []rune
	err      string
}

func newAuthOverlay(provider, model string) *authOverlay {
	return &authOverlay{provider: provider, model: model}
}

func newTaskAuthOverlay(provider, model, role string) *authOverlay {
	return &authOverlay{provider: provider, model: model, role: role}
}

func (a *authOverlay) View(styles Styles, width int) string {
	masked := strings.Repeat("•", len(a.key))
	if len(a.key) == 0 {
		masked = styles.InputHint.Render("type API key")
	}
	var b strings.Builder
	b.WriteString(styles.DialogTitle("Connect provider") + "\n\n")
	b.WriteString(styles.SidebarItem.Render("Provider") + "  " + styles.SidebarActive.Render(safeIDEPlainText(a.provider)) + "\n")
	if a.model != "" {
		b.WriteString(styles.SidebarItem.Render("Model") + "     " + styles.Hint.Render(safeIDEPlainText(a.model)) + "\n")
	}
	if a.role != "" {
		b.WriteString(styles.SidebarItem.Render("Task") + "      " + styles.Hint.Render(safeIDEPlainText(a.role)) + "\n")
	}
	b.WriteString("\n")
	b.WriteString(styles.Hint.Render("API key") + "\n")
	b.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Border(lipgloss.NormalBorder(), false, false, true, false).Width(max(width-4, 1)).MaxWidth(max(width-4, 1)).Render(masked) + "\n")
	if a.err != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenSash)).Render(safeIDEPlainText(a.err)) + "\n")
	}
	b.WriteString("\n" + styles.Hint.Render("enter save securely · esc cancel"))
	return b.String()
}

func (a *authOverlay) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		a.key = nil
		m.overlay = overlayNone
	case tea.KeyBackspace:
		if len(a.key) > 0 {
			a.key = a.key[:len(a.key)-1]
		}
	case tea.KeyEnter:
		key := strings.TrimSpace(string(a.key))
		if key == "" {
			a.err = "API key is required"
			return nil
		}
		if err := m.orch.AuthAPIKey(m.ctx(), a.provider, key); err != nil {
			a.err = err.Error()
			return nil
		}
		if a.model != "" && a.role != "" {
			if err := m.orch.SetTaskModel(m.ctx(), a.role, "native", "", a.model); err != nil {
				a.err = err.Error()
				return nil
			}
		} else if a.model != "" {
			if err := m.orch.SetActiveModel(m.ctx(), a.model); err != nil {
				a.err = err.Error()
				return nil
			}
			m.appendSystem("model: " + safeIDEPlainText(a.model))
		}
		m.status.pushToast("success", "provider connected · "+safeIDEPlainText(a.provider), 2*time.Second)
		a.key = nil
		m.overlay = overlayNone
	case tea.KeySpace:
		a.key = append(a.key, ' ')
	case tea.KeyRunes:
		for _, r := range msg.Runes {
			if r >= 0x20 && r != 0x7f {
				a.key = append(a.key, r)
			}
		}
	}
	return nil
}
