package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/bryann2k/maestro/internal/proposals"
)

// diffCtx is the number of unchanged context lines shown around each hunk.
const diffCtx = 3

// DiffView renders a proposal as a GitHub-style diff: dual line numbers,
// colored -/+ lines with tinted backgrounds, inline word highlighting on
// paired lines, and a +N/-M summary.
func DiffView(styles Styles, prop *proposals.Proposal, width int) string {
	return diffView(styles, prop, width, -1)
}

func diffView(styles Styles, prop *proposals.Proposal, width, selectedHunk int) string {
	if prop == nil {
		return lipgloss.NewStyle().Foreground(styles.T.Color(TokenSash)).Render("  no proposal")
	}
	var b strings.Builder
	add := 0
	del := 0
	for _, h := range prop.Hunks {
		add += len(h.NewLines)
		del += len(h.OldLines)
	}
	// Long absolute paths are left-truncated so the filename stays visible.
	display := safeIDEPlainText(prop.Path)
	if runes := []rune(display); len(runes) > max(width-18, 20) {
		display = "…" + string(runes[len(runes)-(max(width-18, 20)-1):])
	}
	header := fmt.Sprintf("  %s · +%d −%d", display, add, del)
	b.WriteString(lipgloss.NewStyle().Foreground(styles.T.Color(TokenMalibu)).Bold(true).Render(clampANSIWidth(header, width)) + "\n\n")

	base := prop.BaseLines
	delta := 0
	for hunkIndex, h := range prop.Hunks {
		oldStart := h.Start
		newStart := h.Start + delta
		b.WriteString(diffHunkHeader(styles, h, newStart, width, hunkIndex == selectedHunk) + "\n")

		// Context before the hunk.
		ctx := diffCtx
		if oldStart-1 < ctx {
			ctx = oldStart - 1
		}
		ctxOld, ctxNew := oldStart-ctx, newStart-ctx
		for i := 0; i < ctx; i++ {
			b.WriteString(diffContextLine(styles, ctxOld+i, ctxNew+i, base[ctxOld-1+i], width) + "\n")
		}

		// Removed lines, paired with added lines for inline highlighting.
		pairs := len(h.OldLines)
		if len(h.NewLines) > pairs {
			pairs = len(h.NewLines)
		}
		oldNum, newNum := oldStart, newStart
		for i := 0; i < pairs; i++ {
			hasOld := i < len(h.OldLines)
			hasNew := i < len(h.NewLines)
			switch {
			case hasOld && hasNew:
				o, n := h.OldLines[i], h.NewLines[i]
				op, np := inlinePair(o, n)
				b.WriteString(diffOldLine(styles, oldNum, o, op, width) + "\n")
				b.WriteString(diffNewLine(styles, newNum, n, np, width) + "\n")
				oldNum++
				newNum++
			case hasOld:
				b.WriteString(diffOldLine(styles, oldNum, h.OldLines[i], inlineRange{}, width) + "\n")
				oldNum++
			default:
				b.WriteString(diffNewLine(styles, newNum, h.NewLines[i], inlineRange{}, width) + "\n")
				newNum++
			}
		}

		// Context after the hunk.
		afterStart := oldStart + len(h.OldLines) - 1 // 1-based last old line
		remaining := len(base) - afterStart
		after := diffCtx
		if remaining < after {
			after = max(remaining, 0)
		}
		for i := 1; i <= after; i++ {
			idx := afterStart + i
			if idx > len(base) {
				break
			}
			b.WriteString(diffContextLine(styles, oldNum, newNum, base[idx-1], width) + "\n")
			oldNum++
			newNum++
		}

		delta += len(h.NewLines) - len(h.OldLines)
		if len(prop.Hunks) > 1 {
			b.WriteString("\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func diffHunkHeader(styles Styles, h proposals.Hunk, newStart int, width int, selected bool) string {
	oldLen, newLen := len(h.OldLines), len(h.NewLines)
	prefix := "  "
	color := styles.T.Color(TokenMalibu)
	if selected {
		prefix = "▸ "
		color = styles.T.Color(TokenDolly)
	}
	line := fmt.Sprintf("%s@@ -%d,%d +%d,%d @@", prefix, h.Start, oldLen, newStart, newLen)
	return clampANSIWidth(lipgloss.NewStyle().Foreground(color).Bold(selected).Render(line), width)
}

func diffContextLine(styles Styles, oldNum, newNum int, content string, width int) string {
	prefix := diffNums(oldNum, newNum)
	line := prefix + "  " + safeIDEPlainText(content)
	return clampANSIWidth(lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke)).Render(line), width)
}

func diffOldLine(styles Styles, oldNum int, content string, inline inlineRange, width int) string {
	bg := styles.T.Blend(TokenPanel, TokenSash, 0.22)
	prefix := diffNums(oldNum, 0)
	head := lipgloss.NewStyle().Background(bg).Foreground(styles.T.Color(TokenSash)).Render(prefix + "- ")
	return clampANSIWidth(head+diffInline(styles, content, inline, bg, false), width)
}

func diffNewLine(styles Styles, newNum int, content string, inline inlineRange, width int) string {
	bg := styles.T.Blend(TokenPanel, TokenJulep, 0.22)
	prefix := diffNums(0, newNum)
	head := lipgloss.NewStyle().Background(bg).Foreground(styles.T.Color(TokenJulep)).Render(prefix + "+ ")
	return clampANSIWidth(head+diffInline(styles, content, inline, bg, true), width)
}

// inlineRange is a half-open range of rune indexes in one diff line.
type inlineRange struct {
	start int
	end   int
}

// diffInline renders the changed middle segment of a paired line in a
// brighter tone without changing its plain-text content.
func diffInline(styles Styles, content string, inline inlineRange, bg color.Color, isNew bool) string {
	// Repository text must never inherit the terminal's foreground. Prefixes
	// keep their semantic red/green cue; the body uses the theme's primary ink
	// on both the quiet and hot diff backgrounds in every color profile.
	inner := lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Background(bg)
	hot := bg
	if isNew {
		hot = styles.T.Blend(TokenPanel, TokenJulep, 0.5)
	} else {
		hot = styles.T.Blend(TokenPanel, TokenSash, 0.5)
	}
	hotStyle := lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Background(hot).Bold(true)
	// Proposal/base lines are unstyled repository text. Project them before
	// lipgloss adds trusted ANSI; the one-rune projection preserves the inline
	// ranges calculated from the source text.
	runes := []rune(safeIDEPlainText(content))
	start := clamp(inline.start, 0, len(runes))
	end := clamp(inline.end, start, len(runes))
	if start == end {
		return inner.Render(string(runes))
	}
	return inner.Render(string(runes[:start])) +
		hotStyle.Render(string(runes[start:end])) +
		inner.Render(string(runes[end:]))
}

// inlinePair isolates the differing middle of two lines (common prefix and
// suffix removed). Its ranges are expressed in runes, never UTF-8 bytes.
func inlinePair(old, new string) (inlineRange, inlineRange) {
	oldRunes := []rune(old)
	newRunes := []rune(new)
	prefix := 0
	for prefix < len(oldRunes) && prefix < len(newRunes) && oldRunes[prefix] == newRunes[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldRunes)-prefix && suffix < len(newRunes)-prefix &&
		oldRunes[len(oldRunes)-1-suffix] == newRunes[len(newRunes)-1-suffix] {
		suffix++
	}
	return inlineRange{start: prefix, end: len(oldRunes) - suffix},
		inlineRange{start: prefix, end: len(newRunes) - suffix}
}

// diffNums renders the dual line-number gutter ("  12    5 ").
func diffNums(oldNum, newNum int) string {
	oldS := ""
	if oldNum > 0 {
		oldS = fmt.Sprintf("%d", oldNum)
	}
	newS := ""
	if newNum > 0 {
		newS = fmt.Sprintf("%d", newNum)
	}
	return fmt.Sprintf("%4s %4s ", oldS, newS)
}

// diffOverlay is the scrollable full-proposal viewer opened from a proposed
// card.
type diffOverlay struct {
	prop     *proposals.Proposal
	lines    []string
	scroll   int
	maxLines int
}

const diffOverlayFooter = "↑/↓ scroll · → IDE · a accept · d decline · esc close"

// newDiffOverlay builds the overlay and snapshots the rendered lines.
func newDiffOverlay(styles Styles, prop *proposals.Proposal, width int) *diffOverlay {
	o := &diffOverlay{prop: prop}
	o.refresh(styles, width)
	return o
}

// refresh re-renders the diff lines at a new width.
func (o *diffOverlay) refresh(styles Styles, width int) {
	o.lines = strings.Split(DiffView(styles, o.prop, width), "\n")
	if o.scroll > len(o.lines) {
		o.scroll = 0
	}
}

// View renders the visible window of the diff.
func (o *diffOverlay) View(styles Styles, width int) string {
	o.refresh(styles, width)
	visible := o.maxLines
	if visible <= 0 {
		visible = 18
	}
	maxScroll := max(len(o.lines)-visible, 0)
	o.scroll = clamp(o.scroll, 0, maxScroll)
	var b strings.Builder
	if o.scroll > 0 {
		b.WriteString(styles.Hint.Render("↑ …") + "\n")
	}
	for i := o.scroll; i < len(o.lines) && i < o.scroll+visible; i++ {
		b.WriteString(clampANSIWidth(o.lines[i], max(width-4, 10)) + "\n")
	}
	if o.scroll+visible < len(o.lines) {
		b.WriteString(styles.Hint.Render("↓ …") + "\n")
	}
	footer := diffOverlayFooter
	if proposalRequiresAtomicDecision(o.prop) {
		footer = "atomic contract · a accept all · d decline all · esc close"
	}
	if width < 60 {
		if proposalRequiresAtomicDecision(o.prop) {
			footer = "atomic · a all · d all · esc"
		} else {
			footer = "esc close · a accept · d decline · ↑/↓"
		}
	}
	b.WriteString("\n" + styles.Hint.Render(footer))
	return b.String()
}

// scrollBy moves the visible window.
func (o *diffOverlay) scrollBy(delta int) {
	o.scroll = max(o.scroll+delta, 0)
}
