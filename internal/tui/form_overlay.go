package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

const maxFormFieldRunes = 4096

// formAction identifies the product operation that owns a generic form.
// Keeping this outside formOverlay lets the widget stay reusable and keeps
// repository/session mutations in the root Model event loop.
type formAction int

const (
	formActionNone formAction = iota
	formActionBootstrap
	formActionOnboard
	formActionRenameSession
	formActionCreateWorkspace
)

// formField is one short, single-line answer in an interactive command
// wizard. Values stay plain text: rendering applies styles only after the
// same terminal-safe input projection used by the main composer.
type formField struct {
	Key         string
	Label       string
	Help        string
	Value       string
	Placeholder string
	Required    bool
	cursor      int // rune offset, never a byte offset
}

// formOverlay is a compact, keyboard-complete form shared by bootstrap,
// session rename and workspace creation. It deliberately owns no product
// behavior; completion returns a validated map to the calling Model.
type formOverlay struct {
	title  string
	fields []formField
	active int
	err    string
}

func newFormOverlay(title string, fields []formField) *formOverlay {
	copyFields := append([]formField(nil), fields...)
	for i := range copyFields {
		copyFields[i].Value = sanitizeSingleLineInput(copyFields[i].Value)
		if runes := []rune(copyFields[i].Value); len(runes) > maxFormFieldRunes {
			copyFields[i].Value = string(runes[:maxFormFieldRunes])
		}
		copyFields[i].cursor = utf8.RuneCountInString(copyFields[i].Value)
	}
	return &formOverlay{title: title, fields: copyFields}
}

func (f *formOverlay) values() map[string]string {
	values := make(map[string]string, len(f.fields))
	for _, field := range f.fields {
		values[field.Key] = strings.TrimSpace(field.Value)
	}
	return values
}

func (f *formOverlay) validate() bool {
	for i := range f.fields {
		if f.fields[i].Required && strings.TrimSpace(f.fields[i].Value) == "" {
			f.active = i
			f.err = f.fields[i].Label + " is required"
			return false
		}
	}
	f.err = ""
	return true
}

// update returns submitted/cancelled. The caller remains responsible for
// closing the overlay and starting any asynchronous work.
func (f *formOverlay) update(msg tea.KeyMsg) (submitted, cancelled bool) {
	if len(f.fields) == 0 {
		return true, false
	}
	field := &f.fields[f.active]
	runes := []rune(field.Value)
	field.cursor = clamp(field.cursor, 0, len(runes))

	switch msg.Type {
	case tea.KeyEsc:
		return false, true
	case tea.KeyTab, tea.KeyDown:
		f.err = ""
		f.active = (f.active + 1) % len(f.fields)
		return false, false
	case tea.KeyShiftTab, tea.KeyUp:
		f.err = ""
		f.active = (f.active - 1 + len(f.fields)) % len(f.fields)
		return false, false
	case tea.KeyEnter:
		if field.Required && strings.TrimSpace(field.Value) == "" {
			f.err = field.Label + " is required"
			return false, false
		}
		f.err = ""
		if f.active < len(f.fields)-1 {
			f.active++
			return false, false
		}
		return f.validate(), false
	case tea.KeyLeft:
		field.cursor = max(field.cursor-1, 0)
	case tea.KeyRight:
		field.cursor = min(field.cursor+1, len(runes))
	case tea.KeyHome, tea.KeyCtrlA:
		field.cursor = 0
	case tea.KeyEnd, tea.KeyCtrlE:
		field.cursor = len(runes)
	case tea.KeyBackspace:
		if field.cursor > 0 {
			runes = append(runes[:field.cursor-1], runes[field.cursor:]...)
			field.cursor--
			field.Value = string(runes)
		}
	case tea.KeyDelete:
		if field.cursor < len(runes) {
			runes = append(runes[:field.cursor], runes[field.cursor+1:]...)
			field.Value = string(runes)
		}
	case tea.KeyCtrlU:
		runes = runes[field.cursor:]
		field.cursor = 0
		field.Value = string(runes)
	case tea.KeySpace:
		field.insert(" ")
	case tea.KeyRunes:
		field.insert(sanitizeSingleLineInput(string(msg.Runes)))
	}
	return false, false
}

func (f *formField) insert(value string) {
	if value == "" {
		return
	}
	runes := []rune(f.Value)
	f.cursor = clamp(f.cursor, 0, len(runes))
	insert := []rune(value)
	next := make([]rune, 0, len(runes)+len(insert))
	next = append(next, runes[:f.cursor]...)
	next = append(next, insert...)
	next = append(next, runes[f.cursor:]...)
	if len(next) > maxFormFieldRunes {
		next = next[:maxFormFieldRunes]
	}
	f.Value = string(next)
	f.cursor = min(f.cursor+len(insert), len(next))
}

func (f *formOverlay) View(styles Styles, width int) string {
	width = clamp(width, 24, 72)
	var b strings.Builder
	b.WriteString(styles.DialogTitle(f.title) + "\n")
	if len(f.fields) == 0 {
		return b.String()
	}
	current := &f.fields[f.active]
	progress := fmt.Sprintf("%d/%d", f.active+1, len(f.fields))
	b.WriteString(styles.Hint.Render(progress+" · ↑/↓ move between answers") + "\n\n")

	// A three-row window keeps the form usable on 40x10 terminals while the
	// progress counter makes the complete questionnaire explicit.
	start := clamp(f.active-1, 0, max(len(f.fields)-3, 0))
	end := min(start+3, len(f.fields))
	for i := start; i < end; i++ {
		field := &f.fields[i]
		marker := "  "
		labelStyle := styles.SidebarItem
		if i == f.active {
			marker = "▸ "
			labelStyle = styles.SidebarActive
		}
		required := ""
		if field.Required {
			required = " *"
		}
		b.WriteString(labelStyle.Render(marker+field.Label+required) + "\n")
		value := field.Value
		if i == f.active {
			value = renderFormCursor(field.Value, field.cursor)
		} else if strings.TrimSpace(value) == "" {
			value = field.Placeholder
		}
		if value == "" {
			value = "—"
		}
		value = safeIDEPlainText(value)
		b.WriteString("  " + clampANSIWidth(value, max(width-4, 1)) + "\n")
	}

	if current.Help != "" {
		b.WriteString("\n" + styles.Hint.Render(truncateRunes(current.Help, max(width-2, 1))))
	}
	if f.err != "" {
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(styles.T.Color(TokenSash)).Render(truncateRunes(f.err, max(width-2, 1))))
	}
	b.WriteString("\n" + styles.Hint.Render("enter next/save · tab move · esc cancel"))
	return b.String()
}

func renderFormCursor(value string, cursor int) string {
	runes := []rune(value)
	cursor = clamp(cursor, 0, len(runes))
	left, right := string(runes[:cursor]), string(runes[cursor:])
	return left + "│" + right
}
