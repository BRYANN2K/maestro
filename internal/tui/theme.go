// Package tui is Maestro's charm.land v2 frontend: a 70/30 chat interface
// with tool cards, preview-then-accept edits, HITL panel, and pickers.
// Keyboard-first, mouse-complete (§5.10). B11: premium lifting — semantic
// styles, statusline, dialogs, markdown, motion.
package tui

import (
	"fmt"
	"image/color"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lucasb-eyer/go-colorful"
	"github.com/muesli/termenv"

	"github.com/bryann2k/maestro/internal/editor"
)

// Token is a named palette token (Charmtone, §6).
type Token int

// Charmtone tokens (a restrained ink UI with one primary accent).
const (
	TokenCharple  Token = iota // primary coral
	TokenDolly                 // light coral
	TokenBok                   // pink
	TokenBlush                 // soft pink
	TokenSash                  // red
	TokenSquid                 // magenta
	TokenSmoke                 // light gray
	TokenOyster                // cream
	TokenPepper                // deep warm dark (bg)
	TokenChar                  // near-black
	TokenIron                  // mid gray
	TokenCoral                 // orange
	TokenSriracha              // orange-red
	TokenMustard               // yellow
	TokenTang                  // amber
	TokenCitron                // lime
	TokenMalibu                // blue
	TokenJulep                 // mint
	TokenGuac                  // olive
	TokenSurface               // full-screen app background
	TokenPanel                 // panel background
	tokenCount
)

// themeTokenHex is deliberately exhaustive. A built-in theme must define all
// semantic tokens itself: inheriting the missing half of Charmtone made a
// theme look correct in the shell while Markdown, diffs and the IDE silently
// retained coral/olive colors from another palette.
var themeTokenHex = map[string]map[Token]string{
	"charmtone": {
		TokenCharple: "#FF6363", TokenDolly: "#FF8A7A", TokenBok: "#F1A7A0", TokenBlush: "#FFB4AA",
		TokenSash: "#FF716A", TokenSquid: "#B99AF2", TokenSmoke: "#9BA3AE", TokenOyster: "#E4E7EB",
		TokenPepper: "#090B0F", TokenChar: "#05070A", TokenIron: "#5F6875", TokenCoral: "#E98A68",
		TokenSriracha: "#F2766E", TokenMustard: "#DDB642", TokenTang: "#C99D38", TokenCitron: "#86B85C",
		TokenMalibu: "#76AFC4", TokenJulep: "#76B852", TokenGuac: "#8FB56A", TokenSurface: "#07090D",
		TokenPanel: "#0C1016",
	},
	"charmtone-light": {
		TokenCharple: "#9E1B24", TokenDolly: "#8E2633", TokenBok: "#8D1B55", TokenBlush: "#8D3441",
		TokenSash: "#B42318", TokenSquid: "#6938A6", TokenSmoke: "#555A62", TokenOyster: "#1F2328",
		TokenPepper: "#F3F1EC", TokenChar: "#FCFBFA", TokenIron: "#7A7F87", TokenCoral: "#9A3412",
		TokenSriracha: "#A52A2A", TokenMustard: "#7A4A00", TokenTang: "#8A3C10", TokenCitron: "#3E6B00",
		TokenMalibu: "#175CD3", TokenJulep: "#067647", TokenGuac: "#486000", TokenSurface: "#F6F5F2",
		TokenPanel: "#EFEDE8",
	},
	"catppuccin": {
		TokenCharple: "#CBA6F7", TokenDolly: "#B4BEFE", TokenBok: "#F5C2E7", TokenBlush: "#F2B8D5",
		TokenSash: "#F38BA8", TokenSquid: "#CBA6F7", TokenSmoke: "#A6ADC8", TokenOyster: "#CDD6F4",
		TokenPepper: "#1E1E2E", TokenChar: "#181825", TokenIron: "#7F849C", TokenCoral: "#FAB387",
		TokenSriracha: "#F38BA8", TokenMustard: "#F9E2AF", TokenTang: "#F9E2AF", TokenCitron: "#A6E3A1",
		TokenMalibu: "#89B4FA", TokenJulep: "#A6E3A1", TokenGuac: "#94E2D5", TokenSurface: "#1E1E2E",
		TokenPanel: "#313244",
	},
	"tokyo-night": {
		TokenCharple: "#BB9AF7", TokenDolly: "#7AA2F7", TokenBok: "#F7768E", TokenBlush: "#FF9EAA",
		TokenSash: "#F7768E", TokenSquid: "#BB9AF7", TokenSmoke: "#A9B1D6", TokenOyster: "#C0CAF5",
		TokenPepper: "#1A1B26", TokenChar: "#16161E", TokenIron: "#68709A", TokenCoral: "#FF9E64",
		TokenSriracha: "#FF7A93", TokenMustard: "#E0AF68", TokenTang: "#E0AF68", TokenCitron: "#9ECE6A",
		TokenMalibu: "#7DCFFF", TokenJulep: "#9ECE6A", TokenGuac: "#B4F9A5", TokenSurface: "#1A1B26",
		TokenPanel: "#24283B",
	},
	"rose-pine": {
		TokenCharple: "#C4A7E7", TokenDolly: "#9CCFD8", TokenBok: "#EB6F92", TokenBlush: "#F2A4B8",
		TokenSash: "#EB6F92", TokenSquid: "#C4A7E7", TokenSmoke: "#AFA9BB", TokenOyster: "#E0DEF4",
		TokenPepper: "#191724", TokenChar: "#100F17", TokenIron: "#736F83", TokenCoral: "#EA9A97",
		TokenSriracha: "#F0829F", TokenMustard: "#F6C177", TokenTang: "#E8B46B", TokenCitron: "#A8C97F",
		TokenMalibu: "#9CCFD8", TokenJulep: "#7FB4BC", TokenGuac: "#A6C786", TokenSurface: "#191724",
		TokenPanel: "#26233A",
	},
	"nord": {
		TokenCharple: "#88C0D0", TokenDolly: "#81A1C1", TokenBok: "#C8A5C2", TokenBlush: "#D8A0B4",
		TokenSash: "#E0838C", TokenSquid: "#C6A0C0", TokenSmoke: "#AAB4C4", TokenOyster: "#ECEFF4",
		TokenPepper: "#2E3440", TokenChar: "#242933", TokenIron: "#737E91", TokenCoral: "#E09880",
		TokenSriracha: "#E0838C", TokenMustard: "#EBCB8B", TokenTang: "#D9B36C", TokenCitron: "#B8D59C",
		TokenMalibu: "#8FBCBB", TokenJulep: "#A3BE8C", TokenGuac: "#AEC590", TokenSurface: "#242933",
		TokenPanel: "#2E3440",
	},
	"gruvbox": {
		TokenCharple: "#FF6655", TokenDolly: "#DDA0C8", TokenBok: "#E79DBF", TokenBlush: "#F0B0A0",
		TokenSash: "#FF6655", TokenSquid: "#DDA0C8", TokenSmoke: "#B7AA97", TokenOyster: "#FBF1C7",
		TokenPepper: "#282828", TokenChar: "#1D2021", TokenIron: "#7C7165", TokenCoral: "#FE9F69",
		TokenSriracha: "#FF6C5C", TokenMustard: "#FABD2F", TokenTang: "#DFAF45", TokenCitron: "#C5C94A",
		TokenMalibu: "#83A598", TokenJulep: "#B8BB26", TokenGuac: "#A9B665", TokenSurface: "#1D2021",
		TokenPanel: "#282828",
	},
	"one-dark": {
		TokenCharple: "#61AFEF", TokenDolly: "#C678DD", TokenBok: "#E58AC8", TokenBlush: "#EDA4B8",
		TokenSash: "#F07B84", TokenSquid: "#C678DD", TokenSmoke: "#9DA5B4", TokenOyster: "#D7DAE0",
		TokenPepper: "#282C34", TokenChar: "#21252B", TokenIron: "#727A88", TokenCoral: "#E59B67",
		TokenSriracha: "#EF7A75", TokenMustard: "#E5C07B", TokenTang: "#D19A66", TokenCitron: "#A7C77C",
		TokenMalibu: "#61AFEF", TokenJulep: "#98C379", TokenGuac: "#9EBB75", TokenSurface: "#21252B",
		TokenPanel: "#282C34",
	},
}

// Theme is the token → color map. Tokens are stored as hex strings; the
// Color accessor converts to the color.Color used by lipgloss v2 styles.
type Theme struct {
	tokens map[Token]string
}

// Charmtone returns the default theme.
func Charmtone() Theme {
	return themeFromTokens(themeTokenHex["charmtone"])
}

// ThemeForName returns a built-in TUI theme. Unknown names fall back to
// Charmtone so settings files remain forward-compatible. The "auto" theme
// follows the terminal's dark/light background (OSC 11 detection via
// termenv).
func ThemeForName(name string) Theme {
	switch name {
	case "auto":
		if !termenv.HasDarkBackground() {
			return themeFromTokens(themeTokenHex["charmtone-light"])
		}
		return Charmtone()
	case "light":
		name = "charmtone-light"
	}
	if tokens, ok := themeTokenHex[name]; ok {
		return themeFromTokens(tokens)
	}
	return Charmtone()
}

func themeFromTokens(source map[Token]string) Theme {
	t := Theme{tokens: make(map[Token]string, tokenCount)}
	for tok, hex := range source {
		t.tokens[tok] = hex
	}
	return t
}

// ThemeNames lists the built-in TUI theme names (settings picker).
func ThemeNames() []string {
	return []string{
		"auto", "charmtone", "charmtone-light", "catppuccin", "tokyo-night",
		"rose-pine", "nord", "gruvbox", "one-dark",
	}
}

// Color resolves a token.
func (t Theme) Color(tok Token) color.Color {
	if hex, ok := t.tokens[tok]; ok {
		return lipgloss.Color(hex)
	}
	return lipgloss.Color("#E8E6F0")
}

// Hex returns the raw hex of a token ("" when unknown).
func (t Theme) Hex(tok Token) string {
	return t.tokens[tok]
}

// Blend returns a color blended from base toward target by t (0..1).
func (t Theme) Blend(base, target Token, pct float64) color.Color {
	baseHex, targetHex := t.Hex(base), t.Hex(target)
	if baseHex == "" {
		baseHex = themeTokenHex["charmtone"][base]
	}
	if targetHex == "" {
		targetHex = themeTokenHex["charmtone"][target]
	}
	b, _ := colorful.Hex(baseHex)
	tg, _ := colorful.Hex(targetHex)
	return lipgloss.Color(b.BlendHcl(tg, pct).Hex())
}

// gradientText retains the old call surface while rendering a single, quiet
// accent. The product mockups use flat technical color, not decorative type
// gradients.
func gradientText(t Theme, start, _ Token, value string) string {
	return lipgloss.NewStyle().Foreground(t.Color(start)).Render(value)
}

// EditorPalette maps the semantic TUI theme into the embedded editor so all
// built-in themes, including light variants, remain visually coherent.
func (t Theme) EditorPalette() editor.Palette {
	return editor.Palette{
		Accent:    t.Color(TokenCharple),
		CursorInk: t.ContrastOn(t.Color(TokenCharple)),
		Fg:        t.Color(TokenOyster),
		FgSubtle:  t.Color(TokenSmoke),
		// Line numbers are text, not decoration. Keeping them on Smoke avoids
		// sub-WCAG blends that became nearly invisible in Nord/light themes.
		FgVerySubtle: t.Color(TokenSmoke),
		Selection:    t.Blend(TokenPanel, TokenCharple, 0.28),
		Success:      t.Color(TokenJulep),
		Warning:      t.Color(TokenMustard),
		Error:        t.Color(TokenSash),
		Comment:      t.Color(TokenSmoke),
		String:       t.Color(TokenCitron),
		Keyword:      t.Color(TokenSquid),
		Type:         t.Color(TokenMustard),
	}
}

// ContrastOn picks the readable ink (near-black or near-white) for a given
// background color, based on Rec.601 luminance. Used by the powerline
// status segments and colored badges.
func (t Theme) ContrastOn(bg color.Color) color.Color {
	if hex := hexOf(bg); hex != "" {
		if c, err := colorful.Hex(hex); err == nil {
			_, _, l := c.Hsl()
			if l > 0.55 {
				return lipgloss.Color("#1B1B1F")
			}
			return lipgloss.Color("#F5F3EF")
		}
	}
	return lipgloss.Color("#E8E6F0")
}

// hexOf converts a color.Color back to a "#RRGGBB" hex string.
func hexOf(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return ""
	}
	return fmt.Sprintf("#%02X%02X%02X", r>>8, g>>8, b>>8)
}

type Styles struct {
	T Theme

	AppMargin lipgloss.Style

	Panel        lipgloss.Style // base panel: rounded border, alt bg
	PanelFocused lipgloss.Style
	PanelTitle   func(title string) string

	Divider lipgloss.Style // vertical divider between panes

	Header        lipgloss.Style
	HeaderCompact lipgloss.Style
	TabBar        lipgloss.Style
	TabActive     lipgloss.Style
	TabInactive   lipgloss.Style

	MessageUser      lipgloss.Style
	MessageAssistant lipgloss.Style
	MessageSystem    lipgloss.Style
	MessageMuted     lipgloss.Style

	Card         lipgloss.Style
	CardError    lipgloss.Style
	CardRunning  lipgloss.Style
	CardDone     lipgloss.Style
	CardProposed lipgloss.Style

	SidebarSection lipgloss.Style
	SidebarItem    lipgloss.Style
	SidebarActive  lipgloss.Style
	SidebarChecked lipgloss.Style

	Status lipgloss.Style

	Hint lipgloss.Style

	InputFocus lipgloss.Style
	InputBlur  lipgloss.Style
	InputHint  lipgloss.Style

	Button      func(label string, mnemonic byte, focused bool) string
	Dialog      lipgloss.Style
	DialogTitle func(title string) string
}

// NewStyles builds the full semantic style set.
func NewStyles(t Theme) Styles {
	panelTitle := func(title string) string {
		label := lipgloss.NewStyle().Bold(true).Render(gradientText(t, TokenCharple, TokenDolly, title))
		return lipgloss.NewStyle().Padding(0, 1).Render(
			lipgloss.NewStyle().Foreground(t.Color(TokenCharple)).Render("▌ ") + label,
		)
	}
	button := func(label string, mnemonic byte, focused bool) string {
		base := lipgloss.NewStyle().Padding(0, 1).Foreground(t.Color(TokenOyster))
		if focused {
			base = base.Background(t.Color(TokenCharple))
		} else {
			base = base.Border(lipgloss.RoundedBorder()).BorderForeground(t.Color(TokenIron))
		}
		idx := strings.IndexByte(strings.ToLower(label), mnemonic)
		if idx < 0 {
			return base.Render(label)
		}
		runes := []rune(label)
		head := base.Render(string(runes[:idx]))
		mn := base.Underline(true).Render(string(runes[idx]))
		tail := base.Render(string(runes[idx+1:]))
		return head + mn + tail
	}
	return Styles{
		T:         t,
		AppMargin: lipgloss.NewStyle().Padding(0, 1),
		Panel: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(t.Color(TokenIron)).
			Background(t.Color(TokenPanel)).
			Padding(0, 1),
		PanelFocused: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForegroundBlend(t.Color(TokenCharple), t.Color(TokenDolly)).
			Background(t.Color(TokenPanel)).
			Padding(0, 1),
		PanelTitle: panelTitle,
		Divider: lipgloss.NewStyle().
			Border(lipgloss.Border{Right: "│"}).
			BorderForeground(t.Color(TokenIron)),
		Header: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)).
			Padding(0, 1),
		HeaderCompact: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)),
		TabBar: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)).
			Background(t.Color(TokenPanel)),
		TabActive: lipgloss.NewStyle().
			Foreground(t.Color(TokenChar)).
			Background(t.Color(TokenCharple)).
			Bold(true).
			Padding(0, 1),
		TabInactive: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)).
			Background(t.Color(TokenPanel)).
			Padding(0, 1),
		MessageUser: lipgloss.NewStyle().
			Foreground(t.Color(TokenOyster)).
			Border(lipgloss.RoundedBorder(), false, true, false, false).
			BorderForeground(t.Color(TokenCharple)).
			Padding(0, 1, 0, 0),
		MessageAssistant: lipgloss.NewStyle().
			Foreground(t.Color(TokenOyster)),
		MessageSystem: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)).
			Italic(true),
		MessageMuted: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)),
		Card: lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "│"}).
			BorderForeground(t.Color(TokenIron)).
			Background(t.Blend(TokenSurface, TokenPanel, 0.65)).
			Padding(0, 1),
		CardError: lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "│"}).
			BorderForeground(t.Color(TokenSash)).
			Background(t.Blend(TokenSurface, TokenPanel, 0.65)).
			Padding(0, 1),
		CardRunning: lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "│"}).
			BorderForeground(t.Color(TokenTang)).
			Background(t.Blend(TokenSurface, TokenPanel, 0.65)).
			Padding(0, 1),
		CardDone: lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "│"}).
			BorderForeground(t.Color(TokenJulep)).
			Background(t.Blend(TokenSurface, TokenPanel, 0.65)).
			Padding(0, 1),
		CardProposed: lipgloss.NewStyle().
			Border(lipgloss.Border{Left: "│"}).
			BorderForeground(t.Color(TokenMustard)).
			Background(t.Blend(TokenSurface, TokenPanel, 0.65)).
			Padding(0, 1),
		SidebarSection: lipgloss.NewStyle().
			Foreground(t.Color(TokenDolly)).
			Bold(true).
			Padding(0, 1),
		SidebarItem: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)).
			Padding(0, 2),
		SidebarActive: lipgloss.NewStyle().
			Foreground(t.Color(TokenOyster)).
			Background(t.Blend(TokenPanel, TokenCharple, 0.12)).
			Bold(true).
			Padding(0, 1),
		SidebarChecked: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)).
			Padding(0, 2),
		Status: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)).
			Background(t.Color(TokenPanel)),
		Hint: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)),
		InputFocus: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForegroundBlend(t.Color(TokenCharple), t.Color(TokenDolly)).
			Background(t.Color(TokenPanel)).
			Padding(0, 1),
		InputBlur: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(t.Color(TokenIron)).
			Background(t.Color(TokenPanel)).
			Padding(0, 1),
		InputHint: lipgloss.NewStyle().
			Foreground(t.Color(TokenSmoke)),
		Button: button,
		Dialog: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForegroundBlend(t.Color(TokenCharple), t.Color(TokenDolly)).
			Background(t.Color(TokenPanel)).
			Padding(1, 2),
		DialogTitle: func(title string) string {
			return lipgloss.NewStyle().
				Foreground(t.Color(TokenOyster)).
				Bold(true).
				Render(title)
		},
	}
}

// Box renders a bordered panel with an optional title bar.
func (s Styles) Box(name string, content string, width int, focused bool) string {
	return s.box(name, content, width, 0, focused)
}

// BoxSized renders a bordered panel constrained to an exact outer rectangle.
func (s Styles) BoxSized(name string, content string, width, height int, focused bool) string {
	return s.box(name, content, width, height, focused)
}

func (s Styles) box(name string, content string, width, height int, focused bool) string {
	base := s.Panel
	if focused {
		base = s.PanelFocused
	}
	width = max(width, 2)
	inner := content
	if name != "" {
		inner = s.PanelTitle(name) + "\n" + inner
	}
	// Panel styles have a one-cell border and one-cell padding on both
	// sides. lipgloss Width describes the content box, so reserve all four
	// frame cells to keep BoxSized's outer width genuinely exact.
	contentWidth := max(width-4, 1)
	styleWidth := max(width-2, 1)
	inner = clampANSIWidth(inner, contentWidth)
	base = base.Width(styleWidth).MaxWidth(styleWidth)
	if height > 0 {
		height = max(height, 2)
		innerHeight := height - 2
		inner = clampANSIHeight(inner, innerHeight)
		base = base.Height(innerHeight).MaxHeight(innerHeight)
	}
	return base.Render(inner)
}

// clampANSIWidth prevents a child renderer from widening its parent panel.
func clampANSIWidth(content string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	for i := range lines {
		lines[i] = stripBrokenANSI(ansi.Truncate(lines[i], width, ""))
	}
	return strings.Join(lines, "\n")
}

// stripBrokenANSI removes incomplete escape sequences at the end of a line
// (a lone ESC or a CSI without its final byte). A malformed sequence makes
// the terminal desynchronize and display raw bytes literally, so the output
// layer guarantees no broken sequence ever leaves the app.
func stripBrokenANSI(line string) string {
	i := strings.LastIndexByte(line, '\x1b')
	if i < 0 {
		return line
	}
	seq := line[i:]
	if seq == "\x1b" {
		return line[:i]
	}
	if !strings.HasPrefix(seq, "\x1b[") {
		return line
	}
	rest := seq[2:]
	if rest == "" {
		return line[:i]
	}
	j := len(rest)
	for j > 0 && strings.ContainsRune("0123456789;?", rune(rest[j-1])) {
		j--
	}
	if j == 0 {
		// Only parameter bytes follow the CSI introducer: no final byte.
		return line[:i]
	}
	return line
}

func clampANSIHeight(content string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	// A line cut in the middle of an escape sequence would desync the
	// terminal; strip any broken or open sequence from the last kept line.
	lines[len(lines)-1] = stripBrokenANSI(lines[len(lines)-1])
	return strings.Join(lines, "\n")
}

// fitANSIHeight keeps both the top and bottom of a surface when content is
// taller than the terminal. Keeping the tail is important for overlays and
// screens with a footer: a top-only clamp can hide the actionable controls.
func fitANSIHeight(content string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= height {
		return content
	}
	if height == 1 {
		return stripBrokenANSI(lines[0])
	}
	if height == 2 {
		return strings.Join([]string{stripBrokenANSI(lines[0]), stripBrokenANSI(lines[len(lines)-1])}, "\n")
	}
	top := (height - 1) / 2
	bottom := height - top - 1
	kept := make([]string, 0, height)
	kept = append(kept, lines[:top]...)
	kept = append(kept, "  …")
	kept = append(kept, lines[len(lines)-bottom:]...)
	kept[0] = stripBrokenANSI(kept[0])
	kept[len(kept)-1] = stripBrokenANSI(kept[len(kept)-1])
	return strings.Join(kept, "\n")
}

// paintSurface pads and paints the full terminal rectangle so no area is
// left as the terminal's default background ("unloaded" black zones).
func paintSurface(content string, width, height int, bg, fg color.Color) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	content = fitANSIHeight(content, height)
	style := lipgloss.NewStyle().Foreground(fg).Background(bg)
	baseBackground := surfaceBackgroundSGR(bg)
	baseForeground := surfaceForegroundSGR(fg)
	lines := strings.Split(content, "\n")
	painted := make([]string, 0, max(len(lines), height))
	for _, line := range lines {
		line = stripBrokenANSI(ansi.Truncate(line, width, ""))
		// A nested lipgloss span normally ends with SGR 0 (or SGR 49), which
		// also clears the background established by style.Render below. Restore
		// the surface background immediately after those resets so later text
		// cannot fall back to the terminal's default black background.
		line = restoreSurfaceBackground(line, baseBackground)
		// Light themes expose a second form of the same nesting bug: unstyled
		// text, or text following a child span that emits SGR 0/39, inherits the
		// terminal's default ink. That default is commonly near-white even when
		// Maestro paints a light surface. Restore the theme's body ink at the
		// full-frame boundary just as we restore its background.
		line = restoreSurfaceForeground(line, baseForeground)
		if pad := width - ansi.StringWidth(line); pad > 0 {
			// Explicitly select the base background before padding as well. This
			// keeps the right edge painted even when a truncated child style left
			// its own background active.
			line += baseBackground + strings.Repeat(" ", pad)
		}
		painted = append(painted, style.Render(line))
	}
	blank := style.Render(strings.Repeat(" ", width))
	for len(painted) < height {
		painted = append(painted, blank)
	}
	return strings.Join(painted, "\n")
}

// surfaceForegroundSGR returns the full-fidelity foreground sequence paired
// with surfaceBackgroundSGR. The output profile owns terminal downsampling.
func surfaceForegroundSGR(fg color.Color) string {
	if fg == nil {
		return ""
	}
	r, g, b, a := fg.RGBA()
	if a == 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}

// surfaceBackgroundSGR returns the full-fidelity SGR sequence used to repair
// nested resets. Maestro's output boundary performs any ANSI256/ANSI/ASCII
// downsampling, so the model can retain one deterministic truecolor surface.
func surfaceBackgroundSGR(bg color.Color) string {
	if bg == nil {
		return ""
	}
	r, g, b, a := bg.RGBA()
	if a == 0 {
		return ""
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r>>8, g>>8, b>>8)
}

// restoreSurfaceBackground keeps the outer surface active across complete SGR
// sequences embedded by child components. It preserves intentional component
// backgrounds and only repairs sequences whose final background state is the
// terminal default (SGR 0/49).
func restoreSurfaceBackground(line, baseBackground string) string {
	if baseBackground == "" || !strings.Contains(line, "\x1b[") {
		return line
	}
	var out strings.Builder
	out.Grow(len(line) + strings.Count(line, "\x1b[")*len(baseBackground))
	for pos := 0; pos < len(line); {
		rel := strings.Index(line[pos:], "\x1b[")
		if rel < 0 {
			out.WriteString(line[pos:])
			break
		}
		start := pos + rel
		out.WriteString(line[pos:start])
		end := start + 2
		for end < len(line) && (line[end] < 0x40 || line[end] > 0x7e) {
			end++
		}
		if end >= len(line) {
			// stripBrokenANSI normally handles this first; retaining the tail is
			// still safer than manufacturing a sequence if called independently.
			out.WriteString(line[start:])
			break
		}
		out.WriteString(line[start : end+1])
		if line[end] == 'm' && sgrLeavesBackgroundDefault(line[start+2:end]) {
			out.WriteString(baseBackground)
		}
		pos = end + 1
	}
	return out.String()
}

// restoreSurfaceForeground keeps the theme's readable body ink active across
// nested resets. It does not replace an intentional child foreground; only a
// sequence whose final foreground state is the terminal default is repaired.
func restoreSurfaceForeground(line, baseForeground string) string {
	if baseForeground == "" || !strings.Contains(line, "\x1b[") {
		return line
	}
	var out strings.Builder
	out.Grow(len(line) + strings.Count(line, "\x1b[")*len(baseForeground))
	for pos := 0; pos < len(line); {
		rel := strings.Index(line[pos:], "\x1b[")
		if rel < 0 {
			out.WriteString(line[pos:])
			break
		}
		start := pos + rel
		out.WriteString(line[pos:start])
		end := start + 2
		for end < len(line) && (line[end] < 0x40 || line[end] > 0x7e) {
			end++
		}
		if end >= len(line) {
			out.WriteString(line[start:])
			break
		}
		out.WriteString(line[start : end+1])
		if line[end] == 'm' && sgrLeavesForegroundDefault(line[start+2:end]) {
			out.WriteString(baseForeground)
		}
		pos = end + 1
	}
	return out.String()
}

// sgrLeavesBackgroundDefault reports whether an SGR parameter list finishes
// by selecting the terminal's default background. Extended foreground colors
// are skipped as a group so zero-valued RGB channels are never mistaken for
// SGR 0.
func sgrLeavesBackgroundDefault(params string) bool {
	if params == "" {
		return true // ESC[m is equivalent to ESC[0m.
	}
	parts := strings.Split(params, ";")
	changed := false
	defaultBackground := false
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if colon := strings.IndexByte(part, ':'); colon >= 0 {
			part = part[:colon]
		}
		// ECMA-48 defines an omitted SGR parameter as zero. This applies to
		// leading, middle, and trailing empty fields (for example ;31 and
		// 31;), not only to the completely empty ESC[m spelling.
		code := 0
		if part != "" {
			var err error
			code, err = strconv.Atoi(part)
			if err != nil {
				continue
			}
		}
		switch {
		case code == 0 || code == 49:
			changed = true
			defaultBackground = true
		case code >= 40 && code <= 47 || code >= 100 && code <= 107:
			changed = true
			defaultBackground = false
		case code == 38 || code == 48 || code == 58:
			if code == 48 {
				changed = true
				defaultBackground = false
			}
			// Semicolon-form extended colors are 38/48/58;5;n or
			// 38/48/58;2;r;g;b. Colon-form colors are contained in part.
			if strings.Contains(parts[i], ":") || i+1 >= len(parts) {
				continue
			}
			switch parts[i+1] {
			case "5":
				i += min(2, len(parts)-i-1)
			case "2":
				i += min(4, len(parts)-i-1)
			}
		}
	}
	return changed && defaultBackground
}

// sgrLeavesForegroundDefault is the foreground counterpart to
// sgrLeavesBackgroundDefault. Extended background colors are skipped as one
// group so RGB zero channels cannot be mistaken for SGR 0.
func sgrLeavesForegroundDefault(params string) bool {
	if params == "" {
		return true
	}
	parts := strings.Split(params, ";")
	changed := false
	defaultForeground := false
	for i := 0; i < len(parts); i++ {
		part := parts[i]
		if colon := strings.IndexByte(part, ':'); colon >= 0 {
			part = part[:colon]
		}
		code := 0
		if part != "" {
			var err error
			code, err = strconv.Atoi(part)
			if err != nil {
				continue
			}
		}
		switch {
		case code == 0 || code == 39:
			changed = true
			defaultForeground = true
		case code >= 30 && code <= 37 || code >= 90 && code <= 97:
			changed = true
			defaultForeground = false
		case code == 38 || code == 48 || code == 58:
			if code == 38 {
				changed = true
				defaultForeground = false
			}
			if strings.Contains(parts[i], ":") || i+1 >= len(parts) {
				continue
			}
			switch parts[i+1] {
			case "5":
				i += min(2, len(parts)-i-1)
			case "2":
				i += min(4, len(parts)-i-1)
			}
		}
	}
	return changed && defaultForeground
}
