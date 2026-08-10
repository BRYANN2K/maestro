package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// scrollbar renders a proportional ┃ thumb sized from the content and
// viewport heights.
type scrollbar struct {
	height    int
	viewportH int
	contentH  int
	offset    int
	hidden    bool
}

// set updates the scrollbar state.
func (sb *scrollbar) set(height, viewportH, contentH, offset int) {
	sb.height = height
	sb.viewportH = viewportH
	sb.contentH = contentH
	sb.offset = offset
	sb.hidden = contentH <= viewportH
}

// View renders the scrollbar column: only the thumb is drawn — a full "│"
// track on every chat row reads as stray pipes glued to the message text
// (the "salut |" artifact). The track stays empty (spaces) so the text
// zone stays clean while the thumb still signals scrollability.
func (sb *scrollbar) View(styles Styles) string {
	if sb.hidden || sb.height <= 2 {
		return ""
	}
	thumb := lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Render("┃")
	thumbH := sb.viewportH * sb.height / max(sb.contentH, 1)
	if thumbH < 1 {
		thumbH = 1
	}
	if thumbH > sb.height {
		thumbH = sb.height
	}
	maxOffset := max(sb.contentH-sb.viewportH, 1)
	thumbPos := sb.offset * (sb.height - thumbH) / maxOffset

	var b strings.Builder
	for i := 0; i < sb.height; i++ {
		if i >= thumbPos && i < thumbPos+thumbH {
			b.WriteString(thumb + "\n")
		} else {
			b.WriteString(" \n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}
