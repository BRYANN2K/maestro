package tui

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestCharmtoneLightSettingsPreviewUsesReadableInkInProfiledFrame(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 44})

	settings := newSettingsOverlay(m)
	settings.section = settingsAppearance
	settings.focus = settingsFocusContent
	m.overlay, m.overlayM = overlaySettings, settings
	settings.change(m, settingRow{Kind: settingTheme}, 1)
	if got := settings.state.Theme; got != "charmtone-light" {
		t.Fatalf("preview theme = %q, want charmtone-light", got)
	}

	// Exercise the same boundary as cmd/maestro: Model.View emits the complete
	// frame, then colorprofile.Writer resolves it for the terminal.
	var out bytes.Buffer
	writer := colorprofile.Writer{Forward: &out, Profile: colorprofile.TrueColor}
	if _, err := writer.Write([]byte(m.View())); err != nil {
		t.Fatal(err)
	}
	frame := out.String()
	assertRenderedPhraseContrast(t, frame, "charmtone-light", 4.5)
	assertRenderedPhraseContrast(t, frame, "Preview the full TUI and embedded editor", 4.5)
}

type renderedCell struct {
	foreground string
	background string
}

// assertRenderedPhraseContrast simulates the actual SGR foreground/background
// state at every occurrence of phrase. Token-table tests cannot catch text that
// accidentally falls through to the terminal's default foreground.
func assertRenderedPhraseContrast(t *testing.T, frame, phrase string, minimum float64) {
	t.Helper()
	plain, cells := profileTextCells(t, frame)
	found := false
	for from := 0; from <= len(plain)-len(phrase); {
		rel := strings.Index(plain[from:], phrase)
		if rel < 0 {
			break
		}
		found = true
		start := from + rel
		for i := range len(phrase) {
			if phrase[i] == ' ' {
				continue
			}
			cell := cells[start+i]
			if cell.foreground == "" || cell.background == "" {
				t.Fatalf("%q byte %d uses terminal-default color: fg=%q bg=%q", phrase, i, cell.foreground, cell.background)
			}
			if ratio := wcagContrast(cell.foreground, cell.background); ratio < minimum {
				t.Fatalf("%q byte %d contrast %.2f (%s on %s), want >= %.1f", phrase, i, ratio, cell.foreground, cell.background, minimum)
			}
		}
		from = start + len(phrase)
	}
	if !found {
		t.Fatalf("profiled frame does not contain %q", phrase)
	}
}

func profileTextCells(t *testing.T, frame string) (string, []renderedCell) {
	t.Helper()
	var plain strings.Builder
	cells := make([]renderedCell, 0, len(frame))
	state := renderedCell{}
	for pos := 0; pos < len(frame); {
		if strings.HasPrefix(frame[pos:], "\x1b[") {
			end := pos + 2
			for end < len(frame) && (frame[end] < 0x40 || frame[end] > 0x7e) {
				end++
			}
			if end >= len(frame) {
				t.Fatalf("broken ANSI at byte %d", pos)
			}
			if frame[end] == 'm' {
				applyProfileTestSGR(&state, frame[pos+2:end])
			}
			pos = end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(frame[pos:])
		if r == utf8.RuneError && size == 1 {
			t.Fatalf("invalid UTF-8 at byte %d", pos)
		}
		plain.WriteRune(r)
		for range size {
			cells = append(cells, state)
		}
		pos += size
	}
	return plain.String(), cells
}

func applyProfileTestSGR(state *renderedCell, params string) {
	if params == "" {
		*state = renderedCell{}
		return
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		code := 0
		if parts[i] != "" {
			var err error
			code, err = strconv.Atoi(parts[i])
			if err != nil {
				continue
			}
		}
		switch code {
		case 0:
			*state = renderedCell{}
		case 39:
			state.foreground = ""
		case 49:
			state.background = ""
		case 30, 31, 32, 33, 34, 35, 36, 37, 90, 91, 92, 93, 94, 95, 96, 97:
			state.foreground = fmt.Sprintf("ansi:%d", code)
		case 40, 41, 42, 43, 44, 45, 46, 47, 100, 101, 102, 103, 104, 105, 106, 107:
			state.background = fmt.Sprintf("ansi:%d", code)
		case 38, 48:
			if i+4 >= len(parts) || parts[i+1] != "2" {
				continue
			}
			r, errR := strconv.Atoi(parts[i+2])
			g, errG := strconv.Atoi(parts[i+3])
			b, errB := strconv.Atoi(parts[i+4])
			if errR != nil || errG != nil || errB != nil {
				continue
			}
			hex := fmt.Sprintf("#%02X%02X%02X", r, g, b)
			if code == 38 {
				state.foreground = hex
			} else {
				state.background = hex
			}
			i += 4
		}
	}
}

func TestPaintSurfaceRepairsNestedANSIResetSnapshot(t *testing.T) {
	bg := lipgloss.Color("#123456")
	fg := lipgloss.Color("#E0D0C0")
	got := paintSurface("\x1b[31mred\x1b[0mtail", 12, 2, bg, fg)
	base := "\x1b[48;2;18;52;86m"
	ink := "\x1b[38;2;224;208;192m"
	want := "\x1b[38;2;224;208;192;48;2;18;52;86m\x1b[31mred\x1b[0m" + ink + base + "tail" + base + "     \x1b[m\n" +
		"\x1b[38;2;224;208;192;48;2;18;52;86m" + strings.Repeat(" ", 12) + "\x1b[m"
	if got != want {
		t.Fatalf("paintSurface ANSI snapshot mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestPaintSurfaceRepairsOmittedSGRParametersAtExactCell(t *testing.T) {
	bg := lipgloss.Color("#123456")
	fg := lipgloss.Color("#E0D0C0")
	const outer = "\x1b[38;2;224;208;192;48;2;18;52;86m"
	const baseBG = "\x1b[48;2;18;52;86m"
	const baseFG = "\x1b[38;2;224;208;192m"
	tests := []struct {
		name   string
		input  string
		want   string
		cellFG string
		cellBG string
	}{
		{
			name:   "leading omitted parameter resets background then selects red ink",
			input:  "A\x1b[;31mB",
			want:   outer + "A\x1b[;31m" + baseBG + "B\x1b[m",
			cellFG: "ansi:31",
			cellBG: "#123456",
		},
		{
			name:   "trailing omitted parameter resets both colors",
			input:  "A\x1b[31;mB",
			want:   outer + "A\x1b[31;m" + baseFG + baseBG + "B\x1b[m",
			cellFG: "#E0D0C0",
			cellBG: "#123456",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := paintSurface(tt.input, 2, 1, bg, fg)
			if got != tt.want {
				t.Fatalf("omitted-param frame mismatch\n got: %q\nwant: %q", got, tt.want)
			}
			plain, cells := profileTextCells(t, got)
			if plain != "AB" || len(cells) != 2 {
				t.Fatalf("rendered cells = %q %+v", plain, cells)
			}
			if cell := cells[1]; cell.foreground != tt.cellFG || cell.background != tt.cellBG {
				t.Fatalf("B cell = %+v, want fg=%s bg=%s", cell, tt.cellFG, tt.cellBG)
			}
		})
	}
}

func TestRestoreSurfaceForegroundHonorsFinalSGRForeground(t *testing.T) {
	const base = "\x1b[38;2;31;35;40m"
	tests := []struct {
		name string
		seq  string
		want string
	}{
		{name: "implicit reset", seq: "\x1b[m", want: "\x1b[m" + base},
		{name: "full reset", seq: "\x1b[0m", want: "\x1b[0m" + base},
		{name: "default foreground", seq: "\x1b[39m", want: "\x1b[39m" + base},
		{name: "leading omitted then intentional foreground", seq: "\x1b[;31m", want: "\x1b[;31m"},
		{name: "trailing omitted resets foreground", seq: "\x1b[31;m", want: "\x1b[31;m" + base},
		{
			name: "background RGB zero channels are not resets",
			seq:  "\x1b[0;48;2;255;0;0m",
			want: "\x1b[0;48;2;255;0;0m" + base,
		},
		{
			name: "intentional truecolor foreground wins",
			seq:  "\x1b[0;38;2;1;2;3m",
			want: "\x1b[0;38;2;1;2;3m",
		},
		{
			name: "intentional ANSI256 foreground wins",
			seq:  "\x1b[39;38;5;23m",
			want: "\x1b[39;38;5;23m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restoreSurfaceForeground(tt.seq, base); got != tt.want {
				t.Fatalf("restoreSurfaceForeground(%q) = %q, want %q", tt.seq, got, tt.want)
			}
		})
	}
}

func TestRestoreSurfaceBackgroundHonorsFinalSGRBackground(t *testing.T) {
	const base = "\x1b[48;2;18;52;86m"
	tests := []struct {
		name string
		seq  string
		want string
	}{
		{name: "implicit reset", seq: "\x1b[m", want: "\x1b[m" + base},
		{name: "full reset", seq: "\x1b[0m", want: "\x1b[0m" + base},
		{name: "default background", seq: "\x1b[49m", want: "\x1b[49m" + base},
		{name: "leading omitted resets background", seq: "\x1b[;31m", want: "\x1b[;31m" + base},
		{name: "leading omitted then intentional background", seq: "\x1b[;41m", want: "\x1b[;41m"},
		{name: "trailing omitted resets background", seq: "\x1b[31;m", want: "\x1b[31;m" + base},
		{
			name: "foreground RGB zero channels are not resets",
			seq:  "\x1b[0;38;2;255;0;0m",
			want: "\x1b[0;38;2;255;0;0m" + base,
		},
		{
			name: "intentional truecolor background wins",
			seq:  "\x1b[0;48;2;1;2;3m",
			want: "\x1b[0;48;2;1;2;3m",
		},
		{
			name: "intentional ANSI256 background wins",
			seq:  "\x1b[49;48;5;23m",
			want: "\x1b[49;48;5;23m",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := restoreSurfaceBackground(tt.seq, base); got != tt.want {
				t.Fatalf("restoreSurfaceBackground(%q) = %q, want %q", tt.seq, got, tt.want)
			}
		})
	}
}

func TestPaintSurfaceKeepsUnicodeWidthAndProfiles(t *testing.T) {
	raw := paintSurface("界🙂e\u0301 \x1b[0mfin", 14, 2, lipgloss.Color("#123456"), lipgloss.Color("#E0D0C0"))
	for i, line := range strings.Split(raw, "\n") {
		if got := ansi.StringWidth(line); got != 14 {
			t.Fatalf("raw line %d width = %d, want 14: %q", i, got, line)
		}
		if !utf8.ValidString(line) {
			t.Fatalf("raw line %d is invalid UTF-8: %q", i, line)
		}
	}

	profiles := []struct {
		name    string
		profile colorprofile.Profile
		check   func(*testing.T, string)
	}{
		{
			name:    "NO_COLOR ASCII",
			profile: colorprofile.ASCII,
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "38;") || strings.Contains(got, "48;") {
					t.Fatalf("ASCII surface retained a color selection: %q", got)
				}
			},
		},
		{
			name:    "ANSI256",
			profile: colorprofile.ANSI256,
			check: func(t *testing.T, got string) {
				if strings.Contains(got, "48;2;") {
					t.Fatalf("ANSI256 surface retained truecolor: %q", got)
				}
				if !strings.Contains(got, "48;5;") {
					t.Fatalf("ANSI256 surface lost its background: %q", got)
				}
				assertPaintedTerminalCells(t, got, 14, 2)
			},
		},
		{
			name:    "truecolor",
			profile: colorprofile.TrueColor,
			check: func(t *testing.T, got string) {
				if !strings.Contains(got, "\x1b[0m\x1b[38;2;224;208;192m\x1b[48;2;18;52;86mfin") {
					t.Fatalf("truecolor surface did not repair nested reset: %q", got)
				}
				assertPaintedTerminalCells(t, got, 14, 2)
			},
		},
	}
	for _, tt := range profiles {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			writer := colorprofile.Writer{Forward: &out, Profile: tt.profile}
			if _, err := writer.Write([]byte(raw)); err != nil {
				t.Fatal(err)
			}
			got := out.String()
			for i, line := range strings.Split(got, "\n") {
				if width := ansi.StringWidth(line); width != 14 {
					t.Fatalf("profiled line %d width = %d, want 14: %q", i, width, line)
				}
			}
			tt.check(t, got)
		})
	}
}

func TestFullAppSurfaceNeverFallsBackToTerminalBackground(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	cases := []struct {
		name  string
		setup func()
	}{
		{name: "harness", setup: func() { m.SwitchTab(TabHarness) }},
		{
			name: "overlay",
			setup: func() {
				m.SwitchTab(TabHarness)
				m.overlay = overlayPalette
				m.overlayM = newPaletteOverlay(m.orch)
			},
		},
		{
			name: "IDE",
			setup: func() {
				m.overlay = overlayNone
				m.overlayM = nil
				m.SwitchTab(TabIDE)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup()
			assertPaintedTerminalCells(t, m.View(), 140, 38)
		})
	}
}

// assertPaintedTerminalCells simulates only the SGR background state. Every
// visible cell in a full-surface frame must be emitted while a non-default
// background is selected; otherwise terminals such as Ghostty expose black
// rectangles even though the string has the correct dimensions.
func assertPaintedTerminalCells(t *testing.T, frame string, width, height int) {
	t.Helper()
	lines := strings.Split(frame, "\n")
	if len(lines) != height {
		t.Fatalf("surface lines = %d, want %d", len(lines), height)
	}
	for row, line := range lines {
		backgroundSet := false
		column := 0
		for pos := 0; pos < len(line); {
			if strings.HasPrefix(line[pos:], "\x1b[") {
				end := pos + 2
				for end < len(line) && (line[end] < 0x40 || line[end] > 0x7e) {
					end++
				}
				if end >= len(line) {
					t.Fatalf("row %d has broken ANSI at byte %d", row, pos)
				}
				if line[end] == 'm' {
					backgroundSet = applyTestSGRBackground(line[pos+2:end], backgroundSet)
				}
				pos = end + 1
				continue
			}
			r, size := utf8.DecodeRuneInString(line[pos:])
			if r == utf8.RuneError && size == 1 {
				t.Fatalf("row %d contains invalid UTF-8 at byte %d", row, pos)
			}
			if !backgroundSet {
				t.Fatalf("row %d column %d is rendered on terminal default background: %q", row, column, stripANSI(line))
			}
			column += ansi.StringWidth(string(r))
			pos += size
		}
		if column != width {
			t.Fatalf("row %d width = %d, want %d", row, column, width)
		}
	}
}

func applyTestSGRBackground(params string, current bool) bool {
	if params == "" {
		return false
	}
	parts := strings.Split(params, ";")
	for i := 0; i < len(parts); i++ {
		switch parts[i] {
		case "", "0", "49":
			current = false
		case "40", "41", "42", "43", "44", "45", "46", "47",
			"100", "101", "102", "103", "104", "105", "106", "107":
			current = true
		case "38", "48", "58":
			if parts[i] == "48" {
				current = true
			}
			if i+1 < len(parts) {
				switch parts[i+1] {
				case "5":
					i += min(2, len(parts)-i-1)
				case "2":
					i += min(4, len(parts)-i-1)
				}
			}
		}
	}
	return current
}
