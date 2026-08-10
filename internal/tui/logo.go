package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
)

// maestroCompactMark is the smallest identity Maestro renders. It uses only
// single-column ASCII so it remains aligned in every terminal and in
// NO_COLOR mode. The cue remains visible even when the full score cannot be.
const maestroCompactMark = "M> MAESTRO"

// maestroLogo renders Maestro's terminal-native wordmark. The mark combines
// five staff lines, an angular M and a single cue: code in concert. It is the
// terminal interpretation of the approved "The Score" identity, not a second
// logo. It intentionally uses ASCII only; Unicode box/icon widths are
// terminal- and font-dependent and are not suitable for a layout anchor.
//
// Callers receive unpadded content whose visual width never exceeds width.
// The three forms make the identity legible from a 10-column recovery canvas
// through the normal empty state without stealing space from the transcript.
func maestroLogo(styles Styles, width, height int) string {
	width = max(width, 1)
	if width < 18 || height < 6 {
		return maestroCompactMark
	}
	if width < 64 || height < 12 {
		return maestroLogoCompact(styles, width)
	}
	if width >= 72 && height >= 18 {
		return maestroLogoWide(styles, width)
	}
	return maestroLogoStandard(styles, width)
}

func maestroLogoCompact(styles Styles, width int) string {
	accent, score, _, body := maestroLogoStyles(styles)
	lines := []string{
		score.Render("M") + accent.Render(">") + " " + maestroWordmark(styles) + "  " + maestroReady(styles),
		body.Render("Discuss first. Then /propose."),
	}
	return maestroLogoClamp(strings.Join(lines, "\n"), width)
}

func maestroLogoStandard(styles Styles, width int) string {
	accent, score, muted, body := maestroLogoStyles(styles)
	mark := maestroScoreMark(accent, score)
	lines := []string{
		mark[0] + "   " + maestroWordmark(styles) + "  " + maestroReady(styles),
		mark[1] + "   " + accent.Render("CODE IN CONCERT"),
		mark[2] + "   " + body.Render("spec-driven development"),
		mark[3] + "   " + muted.Render("decisions before edits"),
		mark[4],
		"",
		body.Render("Discuss in read-only mode. /propose makes a reviewed spec."),
		muted.Render("/propose  continue   /ide  code"),
	}
	return maestroLogoClamp(strings.Join(lines, "\n"), width)
}

func maestroLogoWide(styles Styles, width int) string {
	accent, score, muted, body := maestroLogoStyles(styles)
	mark := maestroScoreMark(accent, score)
	lines := []string{
		mark[0] + "   " + maestroWordmark(styles) + "  " + maestroReady(styles),
		mark[1] + "   " + accent.Render("CODE IN CONCERT"),
		mark[2] + "   " + body.Render("spec-driven development"),
		mark[3] + "   " + muted.Render("decisions before edits"),
		mark[4],
		"",
		body.Render("Discuss the change with Maestro in read-only mode. Nothing becomes a"),
		body.Render("spec until you explicitly choose /propose."),
		muted.Render("/propose  use this discussion   /ide  open code"),
	}
	return maestroLogoClamp(strings.Join(lines, "\n"), width)
}

// maestroScoreMark is deliberately five rows: each row is one staff line from
// either side of the approved mark. The bottom row carries the hooked outer
// endpoints and the coral cue at the centre of the M.
func maestroScoreMark(accent, score lipgloss.Style) [5]string {
	return [5]string{
		score.Render("-----\\         /-----"),
		score.Render("------\\       /------"),
		score.Render("-------\\     /-------"),
		score.Render("--------\\   /--------"),
		score.Render(" ---|_   \\") + accent.Render(">") + score.Render("/   _|--- "),
	}
}

func maestroLogoStyles(styles Styles) (accent, score, muted, body lipgloss.Style) {
	accent = lipgloss.NewStyle().Foreground(styles.T.Color(TokenCharple)).Bold(true)
	score = lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Bold(true)
	muted = lipgloss.NewStyle().Foreground(styles.T.Color(TokenSmoke))
	body = lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster))
	return accent, score, muted, body
}

func maestroWordmark(styles Styles) string {
	return lipgloss.NewStyle().Foreground(styles.T.Color(TokenOyster)).Bold(true).Render("MAESTRO")
}

func maestroReady(styles Styles) string {
	return lipgloss.NewStyle().Foreground(styles.T.Color(TokenCitron)).Render("ready")
}

func maestroLogoClamp(value string, width int) string {
	return clampANSIWidth(value, max(width, 1))
}
