package tui

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/colorprofile"
	"github.com/lucasb-eyer/go-colorful"
)

func TestBuiltInThemesDefineEveryTokenExplicitly(t *testing.T) {
	for name, palette := range themeTokenHex {
		t.Run(name, func(t *testing.T) {
			if len(palette) != int(tokenCount) {
				t.Fatalf("palette has %d tokens, want %d", len(palette), tokenCount)
			}
			for token := Token(0); token < tokenCount; token++ {
				hex, ok := palette[token]
				if !ok || hex == "" {
					t.Errorf("token %d is not explicitly defined", token)
					continue
				}
				if _, err := colorful.Hex(hex); err != nil {
					t.Errorf("token %d has invalid color %q: %v", token, hex, err)
				}
			}
		})
	}
}

func TestBuiltInThemeContrastMatrix(t *testing.T) {
	textTokens := []Token{
		TokenCharple, TokenDolly, TokenBok, TokenBlush, TokenSash,
		TokenSquid, TokenSmoke, TokenOyster, TokenCoral, TokenSriracha,
		TokenMustard, TokenTang, TokenCitron, TokenMalibu, TokenJulep, TokenGuac,
	}
	for name := range themeTokenHex {
		t.Run(name, func(t *testing.T) {
			theme := ThemeForName(name)
			for _, background := range []Token{TokenSurface, TokenPanel} {
				for _, foreground := range textTokens {
					if ratio := wcagContrast(theme.Hex(foreground), theme.Hex(background)); ratio < 4.5 {
						t.Errorf("text token %d on %d contrast %.2f, want >= 4.5", foreground, background, ratio)
					}
				}
				for _, border := range []Token{TokenIron, TokenCharple, TokenDolly} {
					if ratio := wcagContrast(theme.Hex(border), theme.Hex(background)); ratio < 3 {
						t.Errorf("focus/border token %d on %d contrast %.2f, want >= 3", border, background, ratio)
					}
				}
			}
			if ratio := wcagContrast(theme.Hex(TokenChar), theme.Hex(TokenCharple)); ratio < 4.5 {
				t.Errorf("selected ink on accent contrast %.2f, want >= 4.5", ratio)
			}
			if ratio := wcagContrast(theme.Hex(TokenChar), theme.Hex(TokenSash)); ratio < 4.5 {
				t.Errorf("error ink on error background contrast %.2f, want >= 4.5", ratio)
			}
		})
	}
}

func wcagContrast(a, b string) float64 {
	ca, errA := colorful.Hex(a)
	cb, errB := colorful.Hex(b)
	if errA != nil || errB != nil {
		return 0
	}
	la, lb := relativeLuminance(ca), relativeLuminance(cb)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func relativeLuminance(c colorful.Color) float64 {
	linear := func(value float64) float64 {
		if value <= 0.04045 {
			return value / 12.92
		}
		return math.Pow((value+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(c.R) + 0.7152*linear(c.G) + 0.0722*linear(c.B)
}

func TestMarkdownStyleContainsNoForeignPaletteColors(t *testing.T) {
	for name := range themeTokenHex {
		t.Run(name, func(t *testing.T) {
			theme := ThemeForName(name)
			allowed := map[string]bool{}
			for token := Token(0); token < tokenCount; token++ {
				allowed[strings.ToUpper(theme.Hex(token))] = true
			}
			style := glamourStyle(theme)
			assertStyleColorsBelongToTheme(t, reflect.ValueOf(style), allowed, "markdown")
			if style.H1.BackgroundColor != nil {
				t.Fatalf("H1 retained a foreign background: %q", *style.H1.BackgroundColor)
			}
			if style.CodeBlock.Chroma == nil {
				t.Fatal("Chroma palette is nil")
			}
			chroma := reflect.ValueOf(*style.CodeBlock.Chroma)
			for i := 0; i < chroma.NumField(); i++ {
				primitive := chroma.Field(i)
				colorField := primitive.FieldByName("Color")
				backgroundField := primitive.FieldByName("BackgroundColor")
				if chroma.Type().Field(i).Name == "Background" {
					if backgroundField.IsNil() {
						t.Error("Chroma background is not explicitly themed")
					}
				} else if colorField.IsNil() {
					t.Errorf("Chroma %s foreground is not explicitly themed", chroma.Type().Field(i).Name)
				}
			}
		})
	}
}

func assertStyleColorsBelongToTheme(t *testing.T, value reflect.Value, allowed map[string]bool, path string) {
	t.Helper()
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < value.NumField(); i++ {
		fieldInfo := value.Type().Field(i)
		field := value.Field(i)
		fieldPath := path + "." + fieldInfo.Name
		if (fieldInfo.Name == "Color" || fieldInfo.Name == "BackgroundColor") && field.Kind() == reflect.Pointer && !field.IsNil() {
			color := strings.ToUpper(field.Elem().String())
			if !allowed[color] {
				t.Errorf("%s uses foreign color %q", fieldPath, color)
			}
			continue
		}
		assertStyleColorsBelongToTheme(t, field, allowed, fieldPath)
	}
}

func TestEveryThemePaintsHarnessOverlayAndIDEFrames(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	for _, name := range ThemeNames() {
		t.Run(name, func(t *testing.T) {
			m.SwitchTab(TabHarness)
			m.overlay = overlayNone
			m.overlayM = nil
			m.applyTheme(name)
			assertPaintedTerminalCells(t, m.View(), 140, 38)

			m.overlay = overlaySettings
			m.overlayM = newSettingsOverlay(m)
			assertPaintedTerminalCells(t, m.View(), 140, 38)
			m.overlay = overlayNone
			m.overlayM = nil

			m.SwitchTab(TabIDE)
			assertPaintedTerminalCells(t, m.View(), 140, 38)
		})
	}
}

func TestApplyThemeImmediatelyRendersTranscript(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 32})
	m.messages = append(m.messages, &Message{Role: "assistant", Text: "# Theme\n\n```go\nfunc main() {}\n```"})
	m.renderMessages()
	before := m.lastContent
	m.applyTheme("tokyo-night")
	if after := m.lastContent; after == before {
		t.Fatal("applyTheme left the already-rendered transcript in the old palette")
	}
	if got := m.md.styles.T.Hex(TokenCharple); got != themeTokenHex["tokyo-night"][TokenCharple] {
		t.Fatalf("markdown theme = %q, want Tokyo Night", got)
	}
}

func TestDiffBodyUsesExplicitThemeForeground(t *testing.T) {
	styles := NewStyles(ThemeForName("rose-pine"))
	background := styles.T.Blend(TokenPanel, TokenJulep, 0.22)
	want := lipgloss.NewStyle().
		Foreground(styles.T.Color(TokenOyster)).
		Background(background).
		Render("repository text")
	if got := diffInline(styles, "repository text", inlineRange{}, background, true); got != want {
		t.Fatalf("diff body inherited terminal ink\n got: %q\nwant: %q", got, want)
	}
}

func TestThemeSettingsPreviewCommitAndRollback(t *testing.T) {
	m, _ := newTestModel(t)
	original := m.orch.SettingsSnapshot().Theme
	row := settingRow{Kind: settingTheme}

	preview := newSettingsOverlay(m)
	preview.change(m, row, 1)
	if preview.state.Theme == original {
		t.Fatal("theme did not preview the next palette")
	}
	if got := m.orch.SettingsSnapshot().Theme; got != original {
		t.Fatalf("preview persisted theme %q before Enter; want %q", got, original)
	}
	preview.update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.styles.T.Hex(TokenCharple); got != ThemeForName(original).Hex(TokenCharple) {
		t.Fatalf("Escape did not restore saved theme: %q", got)
	}

	commit := newSettingsOverlay(m)
	commit.change(m, row, 1)
	want := commit.state.Theme
	commit.selected = 1 // Theme row.
	commit.update(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.orch.SettingsSnapshot().Theme; got != want {
		t.Fatalf("Enter persisted %q, want %q", got, want)
	}
}

func TestNoColorFocusRemainsPrintable(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})

	for focus, label := range map[FocusTarget]string{
		FocusViewport: "READ", FocusInput: "PROMPT", FocusSidebar: "ACTIVITY",
	} {
		m.focus = focus
		if clean := asciiProfile(t, m.View()); !strings.Contains(clean, label+"  ●") {
			t.Errorf("Agent focus %v lacks printable %q marker", focus, label)
		}
	}

	m.SwitchTab(TabIDE)
	for focus, label := range map[ideFocus]string{
		ideEditor: m.ide.Ed.DisplayMode(), ideChat: "ASK", ideTree: "FILES", ideHITL: "ACTIONS",
	} {
		m.ide.Focus = focus
		if clean := asciiProfile(t, m.View()); !strings.Contains(clean, label) {
			t.Errorf("IDE focus %v lacks printable %q marker: %q", focus, label, clean)
		}
	}

	styles := NewStyles(ThemeForName("charmtone"))
	selected, _ := permissionButton(styles, "a", "Allow once", true)
	unselected, _ := permissionButton(styles, "a", "Allow once", false)
	if !strings.HasPrefix(asciiProfile(t, selected), ">") || strings.HasPrefix(asciiProfile(t, unselected), ">") {
		t.Fatalf("permission focus is color-only: selected=%q unselected=%q", asciiProfile(t, selected), asciiProfile(t, unselected))
	}
}

func asciiProfile(t *testing.T, value string) string {
	t.Helper()
	var out bytes.Buffer
	writer := colorprofile.Writer{Forward: &out, Profile: colorprofile.ASCII}
	if _, err := writer.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	return stripANSI(out.String())
}

func TestStreamPulseIsEventDrivenAndThrottled(t *testing.T) {
	m, _ := newTestModel(t)
	start := time.Unix(100, 0)
	m.advanceStreamPulse(start)
	first := m.pulse
	m.advanceStreamPulse(start.Add(349 * time.Millisecond))
	if m.pulse != first {
		t.Fatalf("pulse advanced inside 350ms throttle: %d -> %d", first, m.pulse)
	}
	m.advanceStreamPulse(start.Add(350 * time.Millisecond))
	if m.pulse == first {
		t.Fatal("pulse did not advance on a later real stream event")
	}
}

func TestToastTickerMaintainsSingleInFlightTimer(t *testing.T) {
	m, _ := newTestModel(t)
	m.status.pushToast("info", "one", time.Hour)
	if cmd := m.frameTicks(); cmd == nil || !m.toastTickArmed {
		t.Fatal("first toast did not arm its expiry timer")
	}
	if cmd := m.frameTicks(); cmd != nil {
		t.Fatal("second frameTicks call scheduled a duplicate toast timer")
	}

	m.status.toasts[0].Until = time.Now().Add(-time.Second)
	_, cmd := m.Update(toastTickMsg{})
	if len(m.status.toasts) != 0 {
		t.Fatal("expired toast survived its tick")
	}
	if m.toastTickArmed || cmd != nil {
		t.Fatal("expired toast re-armed a timer")
	}
}
