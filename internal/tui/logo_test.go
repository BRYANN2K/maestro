package tui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestMaestroLogoFitsRecoveryAndWelcomeCanvases(t *testing.T) {
	for _, size := range []struct {
		width, height int
	}{
		{10, 4}, {34, 6}, {40, 10}, {80, 24},
	} {
		t.Run("canvas", func(t *testing.T) {
			got := maestroLogo(NewStyles(ThemeForName("charmtone")), size.width, size.height)
			if got == "" {
				t.Fatal("empty logo")
			}
			for row, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w > size.width {
					t.Fatalf("row %d width = %d, want <= %d: %q", row, w, size.width, stripANSI(line))
				}
			}
			if h := lipgloss.Height(got); h > size.height {
				t.Fatalf("height = %d, want <= %d", h, size.height)
			}
		})
	}
}

func TestMaestroLogoUsesOnlySafeSingleColumnASCIIContent(t *testing.T) {
	for _, size := range []struct{ width, height int }{{10, 4}, {34, 6}, {40, 10}, {80, 24}} {
		plain := stripANSI(maestroLogo(NewStyles(ThemeForName("charmtone")), size.width, size.height))
		for _, r := range plain {
			if r == '\n' || (r >= 0x20 && r <= 0x7e) {
				continue
			}
			t.Fatalf("unsafe rune %U at %dx%d", r, size.width, size.height)
		}
		if strings.ContainsRune(plain, '\t') || ansi.StringWidth(plain) < 1 {
			t.Fatalf("unexpected logo whitespace: %q", plain)
		}
	}
}

func TestMaestroLogoIsThemeDriven(t *testing.T) {
	for _, name := range ThemeNames() {
		t.Run(name, func(t *testing.T) {
			styles := NewStyles(ThemeForName(name))
			got := maestroLogo(styles, 80, 24)
			plain := stripANSI(got)
			if !strings.Contains(plain, "MAESTRO") || !strings.Contains(plain, "CODE IN CONCERT") || !strings.Contains(plain, "spec-driven development") {
				t.Fatalf("brand content missing for %s: %q", name, plain)
			}
			if !strings.Contains(got, "\x1b[") {
				t.Fatalf("expected theme color sequences for %s", name)
			}
			for row, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w > 80 {
					t.Fatalf("row %d width=%d > 80", row, w)
				}
			}
		})
	}
}

func TestMaestroScoreMarkKeepsFiveStaffLinesAndOneCue(t *testing.T) {
	styles := NewStyles(ThemeForName("charmtone"))
	accent, score, _, _ := maestroLogoStyles(styles)
	mark := maestroScoreMark(accent, score)
	if len(mark) != 5 {
		t.Fatalf("staff rows = %d, want 5", len(mark))
	}
	plain := stripANSI(strings.Join(mark[:], "\n"))
	if got := strings.Count(plain, ">"); got != 1 {
		t.Fatalf("cue count = %d, want 1: %q", got, plain)
	}
	for row, line := range strings.Split(plain, "\n") {
		if strings.Count(line, "-") < 6 {
			t.Fatalf("staff row %d lost its two score segments: %q", row, line)
		}
		if got := lipgloss.Width(line); got != 21 {
			t.Fatalf("staff row %d width = %d, want 21: %q", row, got, line)
		}
	}
}

func TestMaestroCompactMarkIsTenColumns(t *testing.T) {
	if got := lipgloss.Width(maestroCompactMark); got != 10 {
		t.Fatalf("compact mark width = %d, want 10", got)
	}
}

func TestMaestroLogoDegradesCleanlyForNoColor(t *testing.T) {
	var out bytes.Buffer
	writer := colorprofile.Writer{Forward: &out, Profile: colorprofile.ASCII}
	if _, err := writer.Write([]byte(maestroLogo(NewStyles(ThemeForName("charmtone")), 80, 24))); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "38;") || strings.Contains(got, "48;") {
		t.Fatalf("NO_COLOR projection retained a color selection: %q", got)
	}
	if !strings.Contains(got, "-----\\") || !strings.Contains(got, "MAESTRO") || !strings.Contains(got, "CODE IN CONCERT") {
		t.Fatalf("NO_COLOR projection lost wordmark: %q", got)
	}
}
