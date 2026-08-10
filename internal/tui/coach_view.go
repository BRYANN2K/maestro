package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

type coachOffer struct {
	ID       string
	Title    string
	Prompt   string
	Composer string
	Why      string
	DoneWhen string
	Hint     string
	Duration string
	Mode     string
}

type coachAction int

const (
	coachNoop coachAction = iota
	coachStart
	coachComplete
	coachSnooze
	coachClose
)

// coachOverlay is deliberately calm: it is opened explicitly from /learn or
// from a one-line rail offer at a natural breakpoint. It never animates,
// steals focus while typing, or starts a model/tool on its own.
type coachOverlay struct {
	offer    coachOffer
	showHint bool
}

func newCoachOverlay(offer coachOffer) *coachOverlay {
	if offer.Duration == "" {
		offer.Duration = "2 min"
	}
	return &coachOverlay{offer: offer}
}

func (o *coachOverlay) update(msg tea.KeyMsg) coachAction {
	switch msg.Type {
	case tea.KeyEsc:
		return coachClose
	case tea.KeyEnter:
		return coachStart
	case tea.KeyRunes:
		switch strings.ToLower(msg.String()) {
		case "h", "?":
			o.showHint = !o.showHint
		case "d":
			return coachComplete
		case "s":
			return coachSnooze
		}
	}
	return coachNoop
}

func (o *coachOverlay) View(styles Styles, width int) string {
	return o.viewFull(styles, width)
}

// ViewSized keeps every decision affordance visible inside the terminal body.
// The surrounding Dialog consumes four rows (border plus vertical padding),
// leaving only four content rows at Maestro's supported 40x10 minimum.
func (o *coachOverlay) ViewSized(styles Styles, width, height int) string {
	if height <= 0 {
		return o.viewFull(styles, width)
	}
	mediumRows := 6
	if o.showHint {
		mediumRows++
	}
	if height < mediumRows {
		return o.viewCompact(styles, width)
	}
	if height <= 8 {
		return o.viewMedium(styles, width)
	}
	full := o.viewFull(styles, width)
	if height > 0 && lipgloss.Height(full) > height {
		return o.viewMedium(styles, width)
	}
	return full
}

func (o *coachOverlay) viewFull(styles Styles, width int) string {
	width = clamp(width, 28, 68)
	contentW := max(width-2, 1)
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenDolly)).Bold(true)
	title := safeIDEPlainText(strings.TrimSpace(o.offer.Title))
	prompt := safeIDEPlainText(strings.TrimSpace(o.offer.Prompt))
	why := safeIDEPlainText(strings.TrimSpace(o.offer.Why))
	doneWhen := safeIDEPlainText(strings.TrimSpace(o.offer.DoneWhen))
	hint := safeIDEPlainText(strings.TrimSpace(o.offer.Hint))
	duration := safeIDEPlainText(strings.TrimSpace(o.offer.Duration))
	if title == "" {
		title = "Practice the next decision"
	}

	var b strings.Builder
	b.WriteString(accent.Render("∴ MAESTRO COACH") + "  " + styles.Hint.Render(duration))
	if o.offer.Mode != "" {
		b.WriteString("  " + styles.Hint.Render(strings.ToUpper(safeIDEPlainText(o.offer.Mode))))
	}
	b.WriteString("\n\n")
	b.WriteString(styles.PanelTitle(truncateRunes(title, contentW)) + "\n\n")
	b.WriteString(accent.Render("Next:") + " " + wrapPlain(prompt, max(contentW-6, 1)))
	if why != "" {
		b.WriteString("\n\n" + styles.Hint.Render("Why now: "+truncateRunes(why, max(contentW-9, 1))))
	}
	if doneWhen != "" {
		b.WriteString("\n\n" + styles.Hint.Render("Done when: "+truncateRunes(doneWhen, max(contentW-11, 1))))
	}
	if o.showHint {
		if hint == "" {
			hint = "Explain your reasoning before asking Maestro for the answer."
		}
		b.WriteString("\n\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenMustard)).Render("Hint · "+truncateRunes(hint, max(contentW-7, 1))))
	}
	b.WriteString("\n\n" + styles.Hint.Render("enter practice · h hint · d done · s later · esc close"))
	return b.String()
}

func (o *coachOverlay) viewCompact(styles Styles, width int) string {
	width = clamp(width, 28, 68)
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenDolly)).Bold(true)
	title, prompt, why, doneWhen, hint, duration, mode := o.safeFields()
	_ = title // the action is more useful than the lesson title at four rows
	if why == "" {
		why = "current decision"
	}
	if doneWhen == "" {
		doneWhen = "criterion met"
	}
	headerTail := duration
	if o.showHint && hint != "" {
		headerTail += " · Hint: " + hint
	} else if mode != "" {
		headerTail += " · " + strings.ToUpper(mode)
	}
	header := accent.Render("MAESTRO COACH") + " · " + styles.Hint.Render(headerTail)
	next := accent.Render("Next:") + " " + prompt
	fixed := lipgloss.Width("Why:  · Done: ")
	valueW := max(width-fixed, 2)
	whyW := max(valueW/2, 1)
	doneW := max(valueW-whyW, 1)
	criteria := "Why: " + truncateRunes(why, whyW) + " · Done: " + truncateRunes(doneWhen, doneW)
	controls := "enter go h? d done s later esc"
	return strings.Join([]string{
		clampANSIWidth(header, width),
		clampANSIWidth(next, width),
		clampANSIWidth(styles.Hint.Render(criteria), width),
		clampANSIWidth(styles.Hint.Render(controls), width),
	}, "\n")
}

func (o *coachOverlay) viewMedium(styles Styles, width int) string {
	width = clamp(width, 28, 68)
	accent := lipgloss.NewStyle().Foreground(styles.T.Color(TokenDolly)).Bold(true)
	title, prompt, why, doneWhen, hint, duration, mode := o.safeFields()
	header := accent.Render("MAESTRO COACH") + " · " + styles.Hint.Render(duration)
	if mode != "" {
		header += " · " + styles.Hint.Render(strings.ToUpper(mode))
	}
	lines := []string{
		clampANSIWidth(header, width),
		clampANSIWidth(styles.PanelTitle(title), width),
		clampANSIWidth(accent.Render("Next:")+" "+prompt, width),
		clampANSIWidth(styles.Hint.Render("Why: "+why), width),
		clampANSIWidth(styles.Hint.Render("Done: "+doneWhen), width),
	}
	if o.showHint {
		lines = append(lines, clampANSIWidth(styles.Hint.Render("Hint: "+hint), width))
	}
	lines = append(lines, clampANSIWidth(styles.Hint.Render("enter go h? d done s later esc"), width))
	return strings.Join(lines, "\n")
}

func (o *coachOverlay) safeFields() (title, prompt, why, doneWhen, hint, duration, mode string) {
	title = safeIDEPlainText(strings.TrimSpace(o.offer.Title))
	prompt = safeIDEPlainText(strings.TrimSpace(o.offer.Prompt))
	why = safeIDEPlainText(strings.TrimSpace(o.offer.Why))
	doneWhen = safeIDEPlainText(strings.TrimSpace(o.offer.DoneWhen))
	hint = safeIDEPlainText(strings.TrimSpace(o.offer.Hint))
	duration = safeIDEPlainText(strings.TrimSpace(o.offer.Duration))
	mode = safeIDEPlainText(strings.TrimSpace(o.offer.Mode))
	if title == "" {
		title = "Practice the next decision"
	}
	if prompt == "" {
		prompt = "Explain the next decision."
	}
	if hint == "" {
		hint = "Explain your reasoning before asking Maestro."
	}
	if duration == "" {
		duration = "2 min"
	}
	return title, prompt, why, doneWhen, hint, duration, mode
}
