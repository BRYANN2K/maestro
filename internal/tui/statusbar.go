package tui

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var orchestralVerbs = [...]string{
	"Composing",
	"Orchestrating",
	"Conducting",
	"Arranging",
	"Tuning",
	"Harmonizing",
	"Scoring",
	"Rehearsing",
	"Fine-tuning",
	"Practicing",
	"Improvising",
	"Setting the tempo",
}

const orchestralVerbInterval = 6 * time.Second

// statusSeg is one compact statusline field.
type statusSeg struct {
	Text  string
	Color color.Color
	Bold  bool
	Fill  bool
	Right bool
}

// statusBar is the single-row footer. The full keymap lives behind `? help`
// so the composer and the current state no longer compete with a permanent
// wall of shortcuts.
type statusBar struct {
	segs   []statusSeg
	toasts []toast
	width  int
}

// toast is a TTL'd info message shown over the statusline.
type toast struct {
	Level string // error | warn | info | success
	Msg   string
	Until time.Time
}

// newStatusBar builds the bar.
func newStatusBar() *statusBar {
	return &statusBar{}
}

// setSegs replaces the statusline segments.
func (sb *statusBar) setSegs(segs ...statusSeg) {
	sb.segs = segs
}

// pushToast adds a TTL'd message.
func (sb *statusBar) pushToast(level, msg string, ttl time.Duration) {
	sb.toasts = append(sb.toasts, toast{Level: level, Msg: msg, Until: time.Now().Add(ttl)})
	if len(sb.toasts) > 5 {
		sb.toasts = sb.toasts[1:]
	}
}

// tick prunes expired toasts.
func (sb *statusBar) tick(now time.Time) {
	kept := sb.toasts[:0]
	for _, t := range sb.toasts {
		if now.Before(t.Until) {
			kept = append(kept, t)
		}
	}
	sb.toasts = kept
}

// View renders the statusline with an optional toast overlay and a single
// discoverability affordance on the right.
// Fields use restrained foreground color; state remains visible without a row
// of competing filled badges.
func (sb *statusBar) View(styles Styles, width int, m *Model) string {
	sb.width = width
	var parts, rightParts []string
	if len(sb.segs) > 0 {
		for _, seg := range sb.segs {
			bg := seg.Color
			if bg == nil {
				bg = styles.T.Color(TokenIron)
			}
			style := lipgloss.NewStyle().Foreground(bg)
			if seg.Fill {
				style = style.Background(bg).Foreground(styles.T.ContrastOn(bg)).Padding(0, 1)
			}
			if seg.Bold {
				style = style.Bold(true)
			}
			rendered := style.Render(strings.TrimSpace(seg.Text))
			if seg.Right {
				rightParts = append(rightParts, rendered)
			} else {
				parts = append(parts, rendered)
			}
		}
	}
	help := lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke)).Render("? help")
	rightParts = append(rightParts, help)
	line := strings.Join(parts, lipgloss.NewStyle().Foreground(styles.T.Color(TokenIron)).Render("  ·  "))
	if right := strings.Join(rightParts, "  "); right != "" {
		gap := max(width-lipgloss.Width(line)-lipgloss.Width(right)-1, 1)
		line += strings.Repeat(" ", gap) + right
	}
	if line == "" {
		line = " "
	}
	// Toast overlay on the right side.
	if len(sb.toasts) > 0 {
		last := sb.toasts[len(sb.toasts)-1]
		color := styles.T.Color(TokenSmoke)
		switch last.Level {
		case "error":
			color = styles.T.Color(TokenSash)
		case "warn":
			color = styles.T.Color(TokenMustard)
		case "success":
			color = styles.T.Color(TokenJulep)
		}
		toastStyle := lipgloss.NewStyle().Foreground(color).Bold(true)
		msg := truncateRunes(last.Msg, 48)
		separator := lipgloss.NewStyle().Foreground(styles.T.Color(TokenIron)).Render("  ·  ")
		renderedToast := toastStyle.Render(msg)
		// The line already contains styled segments: truncate ANSI-aware so
		// escape sequences are never split, which would desync the terminal.
		available := max(width-lipgloss.Width(separator)-lipgloss.Width(renderedToast), 1)
		line = stripBrokenANSI(ansi.Truncate(line, available, ""))
		line += separator + renderedToast
	}
	line = styles.Status.Width(width).MaxWidth(width).Render(clampANSIWidth(line, width))
	if m.activeTab == TabIDE {
		plain := ansi.Strip(line)
		label := map[bool]string{true: "FOLLOW", false: "FREE"}[m.followAgent]
		if index := strings.Index(plain, label); index >= 0 {
			x := lipgloss.Width(plain[:index])
			m.regions = append(m.regions, Region{X: x, Y: tabBarRows + m.bodyHeight(), W: lipgloss.Width(label), H: 1, Action: ActionToggleFollow, Label: "toggle Follow Maestro", Binding: "/follow"})
		}
	}
	m.regions = append(m.regions, Region{
		X: max(width-lipgloss.Width(help)-1, 0), Y: tabBarRows + m.bodyHeight(),
		W: lipgloss.Width(help), H: 1, Action: ActionKeymap,
		Label: "open keymap", Binding: "space ?",
	})
	return line
}

// renderStatusline assembles the segments from the model state.
func (m *Model) renderStatusline() {
	sb := m.status
	if m.activeTab == TabIDE && m.ide != nil {
		focusLabel := m.ide.Ed.DisplayMode()
		switch m.ide.Focus {
		case ideChat:
			focusLabel = "ASK"
		case ideTree:
			focusLabel = "FILES"
		case ideHITL:
			focusLabel = "ACTIONS"
		}
		segs := []statusSeg{{
			Text:  focusLabel,
			Color: m.styles.T.Color(TokenCharple),
			Bold:  true,
			Fill:  true,
		}}
		file, position := "untitled", "1:1"
		if buf := m.ide.Ed.Buffer(); buf != nil {
			file = buf.Path
			if rel, err := filepath.Rel(m.ide.project, file); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				file = filepath.ToSlash(rel)
			}
			file = safeIDEPlainText(file)
			if buf.Dirty {
				file += " ●"
			}
			position = fmt.Sprintf("%d:%d", buf.Cur.Line+1, buf.Cur.Col+1)
		}
		segs = append(segs,
			statusSeg{Text: file, Color: m.styles.T.Color(TokenSmoke)},
			statusSeg{Text: map[bool]string{true: "FOLLOW", false: "FREE"}[m.followAgent], Color: map[bool]color.Color{true: m.styles.T.Color(TokenJulep), false: m.styles.T.Color(TokenSmoke)}[m.followAgent], Bold: m.followAgent},
			statusSeg{Text: position, Color: m.styles.T.Color(TokenSmoke), Right: true},
		)
		sb.setSegs(segs...)
		return
	}
	elapsed := ""
	if m.busy && !m.runStart.IsZero() {
		elapsed = time.Since(m.runStart).Round(time.Second).String()
	}

	mutedText := m.styles.T.Color(TokenSmoke)
	focusLabel := "READ"
	switch m.focus {
	case FocusInput:
		focusLabel = "PROMPT"
	case FocusSidebar:
		focusLabel = "ACTIVITY"
	}
	segs := []statusSeg{{Text: focusLabel + "  ● Maestro is ready", Color: m.styles.T.Color(TokenJulep), Bold: true}}
	blocking := 0
	for _, it := range m.sidebar.hitl {
		if it.Blocking && !m.sidebar.checked[it.ID] {
			blocking++
		}
	}
	if blocking > 0 {
		segs[0] = statusSeg{Text: fmt.Sprintf("%s  ● Maestro is paused · %d blocking action(s)", focusLabel, blocking), Color: m.styles.T.Color(TokenSash), Bold: true}
	}
	if m.busy {
		segs[0] = statusSeg{
			Text:  fmt.Sprintf("%s  ● Task running · %s", focusLabel, elapsed),
			Color: m.styles.T.Color(TokenCharple),
			Bold:  true,
		}
	}
	if m.hoverMsg != "" {
		segs = append(segs, statusSeg{
			Text:  " " + truncateRunes(m.hoverMsg, 40) + " ",
			Color: mutedText,
		})
	}
	model := safeIDEPlainText(m.orch.ActiveModel())
	if model == "" {
		model = "auto"
	}
	segs = append(segs, statusSeg{
		Text:  truncateRunes(model, 24) + " · " + safeIDEPlainText(m.orch.ActiveReasoningEffort()),
		Color: mutedText,
		Right: true,
	})
	sb.setSegs(segs...)
}

// activity returns the calm orchestral phrase rendered beside Maestro in the
// active message header. It changes slowly; only the ellipsis moves each tick.
func (m *Model) activity() string {
	elapsed := time.Duration(0)
	if !m.runStart.IsZero() {
		elapsed = time.Since(m.runStart)
	}
	index := int(elapsed/orchestralVerbInterval) % len(orchestralVerbs)
	dots := strings.Repeat(".", m.pulse%3+1)
	return orchestralVerbs[index] + dots
}

// ctxChip renders the context meter segment from (used, total) session
// tokens; ok is false when the model's window is unknown.
func ctxChip(used, total int, s Styles) (statusSeg, bool) {
	if total <= 0 {
		return statusSeg{}, false
	}
	pct := used * 100 / total
	if pct > 100 {
		pct = 100
	}
	color := s.T.Color(TokenJulep)
	icon := ""
	switch {
	case pct >= 80:
		color, icon = s.T.Color(TokenSash), "⚠"
	case pct >= 60:
		color = s.T.Color(TokenMustard)
	}
	return statusSeg{Text: fmt.Sprintf(" %sctx %d%% ", icon, pct), Color: color, Bold: pct >= 80}, true
}
