package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/orchestrator"
)

type providerCard struct {
	id, label, kind, status, source, cli string
	installed, connected, requiresKey    bool
	models                               int
}

type providersOverlay struct {
	cards            []providerCard
	selected         int
	query            string
	hits             []workspaceHit
	originX, originY int
}

func newProvidersOverlay(orch *orchestrator.Orchestrator, selectID string) *providersOverlay {
	o := &providersOverlay{}
	for _, sub := range orch.SubscriptionList(context.Background()) {
		o.cards = append(o.cards, providerCard{
			id: sub.ID, label: safeIDEPlainText(sub.Label), kind: "subscription", status: safeIDEPlainText(sub.Status),
			cli: safeIDEPlainText(sub.CLI), installed: sub.Installed, connected: sub.Authenticated,
			models: len(sub.Models),
		})
	}
	seen := map[string]bool{}
	for _, info := range orch.ProviderList(context.Background()) {
		seen[info.Name] = true
		o.cards = append(o.cards, providerCard{
			id: info.Name, label: safeIDEPlainText(info.Name), kind: "api", status: safeIDEPlainText(providerStatus(info)),
			source: safeIDEPlainText(info.Source), installed: true, connected: info.KeySet || !info.RequiresKey,
			requiresKey: info.RequiresKey, models: info.Models,
		})
	}
	counts := map[string]int{}
	for _, model := range orch.ModelList(context.Background()) {
		counts[model.Provider]++
	}
	var catalogIDs []string
	for id := range counts {
		if !seen[id] {
			catalogIDs = append(catalogIDs, id)
		}
	}
	sort.Strings(catalogIDs)
	for _, id := range catalogIDs {
		count := counts[id]
		info, _ := orch.ProviderInfo(context.Background(), id)
		o.cards = append(o.cards, providerCard{
			id: id, label: safeIDEPlainText(id), kind: "api", status: safeIDEPlainText(providerStatus(info)), source: "remote catalog",
			installed: true, connected: info.KeySet || !info.RequiresKey,
			requiresKey: info.RequiresKey, models: count,
		})
	}
	for i, card := range o.cards {
		if card.id == selectID {
			o.selected = i
			break
		}
	}
	return o
}

func providerStatus(info orchestrator.ProviderInfo) string {
	switch {
	case !info.RequiresKey:
		return "ready · local"
	case info.KeySet:
		return "connected · API key"
	default:
		return "API key required"
	}
}

func (o *providersOverlay) View(styles Styles, width int) string {
	return o.viewSized(styles, width, 28)
}

func (o *providersOverlay) filtered() []providerCard {
	if o.query == "" {
		return o.cards
	}
	q := strings.ToLower(o.query)
	var out []providerCard
	for _, card := range o.cards {
		if strings.Contains(strings.ToLower(safeIDEPlainText(card.id)+" "+card.label+" "+card.kind), q) {
			out = append(out, card)
		}
	}
	return out
}

func (o *providersOverlay) current() *providerCard {
	cards := o.filtered()
	if o.selected < 0 || o.selected >= len(cards) {
		return nil
	}
	return &cards[o.selected]
}

func (o *providersOverlay) viewSized(styles Styles, width, height int) string {
	if width < 72 || height < 22 {
		return o.viewCompact(styles, width, height)
	}
	width = max(width, 72)
	height = max(height, 22)
	o.hits = nil
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	muted := styles.Hint
	plain := styles.SidebarItem
	active := styles.SidebarActive
	leftW := max(width*37/100, 28)
	rightW := max(width-leftW-5, 38)
	rows := max(height-9, 10)

	var left strings.Builder
	left.WriteString(accent.Render("PROVIDERS") + "  " + muted.Render("filter: "+o.query) + "\n")
	cards := o.filtered()
	top := centeredWindow(o.selected, len(cards), rows)
	for row := 0; row < rows; row++ {
		i := top + row
		if i >= len(cards) {
			left.WriteString("\n")
			continue
		}
		card := cards[i]
		icon := "○"
		if card.connected {
			icon = "●"
		} else if !card.installed {
			icon = "×"
		}
		kind := "API"
		if card.kind == "subscription" {
			kind = "PLAN"
		}
		line := fmt.Sprintf("%s %-20s %s", icon, truncateRunes(card.label, 20), kind)
		style := plain
		if i == o.selected {
			style = active
		}
		left.WriteString(style.Width(leftW-2).Render(line) + "\n")
		o.hits = append(o.hits, workspaceHit{x: 2, y: 4 + row, w: leftW, h: 1, kind: "provider", index: i})
	}

	var right strings.Builder
	right.WriteString(accent.Render("CONNECTION") + "\n\n")
	card := o.current()
	if card == nil {
		right.WriteString(muted.Render("No provider matches this filter."))
	} else {
		right.WriteString(lipgloss.NewStyle().Bold(true).Foreground(styles.T.Color(TokenOyster)).Render(card.label) + "\n")
		right.WriteString(muted.Render(strings.ToUpper(card.kind)) + "\n\n")
		right.WriteString(muted.Render("STATUS") + "\n")
		statusStyle := lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke))
		if card.connected {
			statusStyle = lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
		}
		right.WriteString(statusStyle.Render(card.status) + "\n\n")
		if card.kind == "subscription" {
			right.WriteString("Uses your official " + card.cli + " CLI session.\n")
			right.WriteString(muted.Render("Credentials stay in the vendor keychain; Maestro never imports tokens.") + "\n\n")
			if !card.installed {
				right.WriteString(muted.Render("Install the "+card.cli+" CLI, then reopen this page.") + "\n")
			} else if card.connected {
				right.WriteString(active.Render("[ m  Assign to task ]") + "  " + plain.Render("[ x  Sign out ]") + "\n")
			} else {
				right.WriteString(active.Render("[ enter  Sign in ]") + "\n")
			}
		} else {
			right.WriteString(fmt.Sprintf("%d models · %s\n", card.models, defaultString(card.source, "remote catalog")))
			right.WriteString(muted.Render("Models remain sourced from the remote models.dev catalog.") + "\n\n")
			if card.requiresKey && !card.connected {
				right.WriteString(active.Render("[ enter  Add API key ]") + "\n")
			} else {
				right.WriteString(active.Render("[ m  Assign models to tasks ]") + "\n")
			}
		}
		o.hits = append(o.hits,
			workspaceHit{x: leftW + 4, y: 12, w: rightW, h: 2, kind: "primary", index: o.selected},
			workspaceHit{x: leftW + 4, y: 13, w: rightW, h: 2, kind: "secondary", index: o.selected},
		)
	}
	columns := lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(leftW).Render(left.String()),
		lipgloss.NewStyle().Foreground(styles.T.Color(TokenIron)).Render("│ "),
		lipgloss.NewStyle().Width(rightW).Render(right.String()),
	)
	footer := muted.Render("↑/↓ browse · enter connect · m models · x logout · r refresh · esc close")
	content := accent.Render("PROVIDER WORKSPACE") + muted.Render("   subscriptions + API catalog") + "\n\n" + columns + "\n" + footer
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(styles.T.Color(TokenIron)).Padding(0, 1).Width(width).Height(height).Render(content)
}

// viewCompact is the narrow-terminal provider workspace. It keeps the
// selected provider, connection state, primary action and close key visible
// in one column so the dialog remains usable at 40x10 and 80x24.
func (o *providersOverlay) viewCompact(styles Styles, width, height int) string {
	width = max(width, 1)
	height = max(height, 1)
	o.hits = nil
	innerW := max(width-2, 1)
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	muted := styles.Hint
	active := styles.SidebarActive

	label, status, action := "no provider", "no matches", "type to filter"
	if card := o.current(); card != nil {
		label, status = card.label, card.status
		switch {
		case card.kind == "subscription" && !card.installed:
			action = "install " + card.cli + " CLI"
		case card.kind == "subscription" && !card.connected:
			action = "enter sign in"
		case card.kind == "api" && card.requiresKey && !card.connected:
			action = "enter add API key"
		default:
			action = "m assign models"
		}
	}
	lines := []string{
		accent.Render("PROVIDERS") + muted.Render("  filter: "+truncateRunes(o.query, max(innerW-20, 1))),
		active.Render(truncateRunes(label, innerW)),
		muted.Render(truncateRunes(status, innerW)),
		accent.Render(truncateRunes(action, innerW)),
		muted.Render("↑/↓ browse · " + "esc close"),
	}
	content := clampANSIHeight(clampANSIWidth(strings.Join(lines, "\n"), innerW), height)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.T.Color(TokenIron)).
		Padding(0, 1).
		Width(width).MaxWidth(width).
		Height(height).MaxHeight(height).
		Render(content)
}

func (o *providersOverlay) primary(m *Model) tea.Cmd {
	card := o.current()
	if card == nil {
		return nil
	}
	if card.kind == "subscription" {
		if !card.installed {
			m.status.pushToast("error", card.cli+" CLI is not installed", 4*time.Second)
			return nil
		}
		if card.connected {
			m.overlay = overlayModelPicker
			m.overlayM = newTaskModelOverlay(m.orch)
			return nil
		}
		return m.runSubscriptionAction(card.id, "login")
	}
	if card.requiresKey && !card.connected {
		m.overlay = overlayAuth
		m.overlayM = newAuthOverlay(card.id, "")
		return nil
	}
	m.overlay = overlayModelPicker
	m.overlayM = newTaskModelOverlay(m.orch)
	return nil
}

func (o *providersOverlay) logout(m *Model) tea.Cmd {
	card := o.current()
	if card == nil || card.kind != "subscription" || !card.connected {
		return nil
	}
	return m.runSubscriptionAction(card.id, "logout")
}

func (o *providersOverlay) update(m *Model, msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyEsc:
		m.overlay = overlayNone
	case tea.KeyUp:
		o.selected = max(o.selected-1, 0)
	case tea.KeyDown:
		o.selected = min(o.selected+1, max(len(o.filtered())-1, 0))
	case tea.KeyBackspace:
		if o.query != "" {
			r := []rune(o.query)
			o.query = string(r[:len(r)-1])
			o.selected = 0
		}
	case tea.KeyEnter:
		return o.primary(m)
	case tea.KeyRunes:
		switch msg.String() {
		case "m":
			m.overlay = overlayModelPicker
			m.overlayM = newTaskModelOverlay(m.orch)
		case "x":
			return o.logout(m)
		case "r":
			return func() tea.Msg { return modelsRefreshedMsg{err: m.orch.RefreshModels(context.Background())} }
		default:
			o.query += sanitizeSingleLineInput(string(msg.Runes))
			o.selected = 0
		}
	}
	return nil
}

func (o *providersOverlay) mouse(m *Model, msg tea.MouseMsg) tea.Cmd {
	if msg.Button == tea.MouseButtonWheelUp || msg.Button == tea.MouseButtonWheelDown {
		delta := 1
		if msg.Button == tea.MouseButtonWheelUp {
			delta = -1
		}
		o.selected = clamp(o.selected+delta, 0, max(len(o.filtered())-1, 0))
		return nil
	}
	if msg.Button != tea.MouseButtonLeft || msg.Action != tea.MouseActionPress {
		return nil
	}
	x, y := msg.X-o.originX, msg.Y-o.originY
	for _, hit := range o.hits {
		if x < hit.x || x >= hit.x+hit.w || y < hit.y || y >= hit.y+hit.h {
			continue
		}
		switch hit.kind {
		case "provider":
			if o.selected == hit.index {
				return o.primary(m)
			}
			o.selected = hit.index
		case "primary":
			return o.primary(m)
		case "secondary":
			return o.logout(m)
		}
		break
	}
	return nil
}

func (m *Model) runSubscriptionAction(provider, action string) tea.Cmd {
	cmd, err := m.orch.SubscriptionCommand(provider, action)
	if err != nil {
		m.status.pushToast("error", err.Error(), 4*time.Second)
		return nil
	}
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return subscriptionActionDoneMsg{provider: provider, action: action, err: err}
	})
}
