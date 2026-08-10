package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFormOverlayNavigationEditingAndValidation(t *testing.T) {
	f := newFormOverlay("Bootstrap", []formField{
		{Key: "name", Label: "Project name", Required: true},
		{Key: "goal", Label: "Outcome", Required: true},
		{Key: "stack", Label: "Stack"},
	})
	if done, _ := f.update(tea.KeyMsg{Type: tea.KeyEnter}); done || f.err == "" {
		t.Fatal("empty required field submitted")
	}
	f.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Mæstro")})
	f.update(tea.KeyMsg{Type: tea.KeyEnter})
	f.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Ship safely")})
	f.update(tea.KeyMsg{Type: tea.KeyEnter})
	f.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("Go + TUI")})
	if done, cancelled := f.update(tea.KeyMsg{Type: tea.KeyEnter}); !done || cancelled {
		t.Fatal("complete form did not submit")
	}
	values := f.values()
	if values["name"] != "Mæstro" || values["goal"] != "Ship safely" || values["stack"] != "Go + TUI" {
		t.Fatalf("values = %#v", values)
	}
}

func TestFormOverlayCursorAndUnsafeInput(t *testing.T) {
	f := newFormOverlay("Rename", []formField{{Key: "title", Label: "Title", Value: "ac", Required: true}})
	f.update(tea.KeyMsg{Type: tea.KeyLeft})
	f.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("b\x1b[31m\a")})
	if got := f.values()["title"]; got != "abc" {
		t.Fatalf("sanitized cursor edit = %q", got)
	}
	f.update(tea.KeyMsg{Type: tea.KeyHome})
	f.update(tea.KeyMsg{Type: tea.KeyDelete})
	if got := f.values()["title"]; got != "bc" {
		t.Fatalf("delete = %q", got)
	}
}

func TestFormOverlayCompactViewAndCancel(t *testing.T) {
	f := newFormOverlay("New workspace", []formField{
		{Key: "branch", Label: "Branch", Value: "feat/東京", Help: "A new linked worktree is created from HEAD."},
		{Key: "name", Label: "Workspace", Placeholder: "derived from branch"},
		{Key: "extra", Label: "Extra"},
		{Key: "fourth", Label: "Fourth"},
	})
	view := stripANSI(f.View(NewStyles(ThemeForName("charmtone")), 40))
	for _, want := range []string{"New workspace", "1/4", "Branch", "feat/東京", "esc cancel"} {
		if !strings.Contains(view, want) {
			t.Fatalf("compact form missing %q:\n%s", want, view)
		}
	}
	if _, cancelled := f.update(tea.KeyMsg{Type: tea.KeyEsc}); !cancelled {
		t.Fatal("esc did not cancel")
	}
}

func TestFormOverlayConsumesMouseInsteadOfClickingBehindIt(t *testing.T) {
	m, _ := newTestModel(t)
	m.SetSize(100, 30)
	m.activeTab = TabHarness
	m.regions = []Region{{X: 2, Y: 2, W: 20, H: 1, Action: ActionSwitchTab, Tab: TabIDE}}
	m.overlay = overlayForm
	m.overlayM = newFormOverlay("Rename", []formField{{Key: "title", Label: "Title"}})

	updated, cmd := m.updateMouse(tea.MouseMsg{X: 4, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if cmd != nil {
		t.Fatal("modal mouse guard returned a command")
	}
	if updated.(*Model).activeTab != TabHarness {
		t.Fatal("click leaked through form overlay to the workspace tab")
	}
}
