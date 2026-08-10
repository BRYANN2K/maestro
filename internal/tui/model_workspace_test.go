package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/settings"
)

func TestTaskModelOverlayPersistsNativeRoute(t *testing.T) {
	m, _ := newTestModel(t)
	o := &taskModelOverlay{
		tasks: taskRoutes,
		task:  1,
		sources: []modelSource{{
			id: "local", label: "Local", kind: "native", ready: true, installed: true,
			models: []routeModel{{id: "local/coder", name: "Coder", efforts: []string{"auto"}}},
		}},
	}
	o.apply(m)
	route := m.orch.SettingsSnapshot().RoleDefaults[settings.RoleDev]
	if route.Engine != "native" || route.Agent != "" || route.Model != "local/coder" || route.ReasoningEffort != "" {
		t.Fatalf("dev route = %+v", route)
	}
}

func TestTaskModelOverlayPersistsSubscriptionRoute(t *testing.T) {
	m, _ := newTestModel(t)
	o := &taskModelOverlay{
		tasks: taskRoutes,
		task:  2,
		sources: []modelSource{{
			id: "codex", label: "Codex", kind: "subscription", agent: "codex",
			ready: true, installed: true, models: []routeModel{{id: "gpt-test", name: "GPT Test", efforts: []string{"auto", "high"}}},
		}},
		reasoning: 1,
	}
	o.apply(m)
	route := m.orch.SettingsSnapshot().RoleDefaults[settings.RoleReviewer]
	if route.Engine != "legacy" || route.Agent != "codex" || route.Model != "gpt-test" || route.ReasoningEffort != "high" {
		t.Fatalf("review route = %+v", route)
	}
}

func TestBuildEnginePickerStartsOnPersistedTaskRoute(t *testing.T) {
	m, _ := newTestModel(t)
	if err := m.orch.SetTaskModel(m.ctx(), settings.RoleDev, "legacy", "claude", "sonnet"); err != nil {
		t.Fatalf("SetTaskModel: %v", err)
	}
	o := newEngineOverlay(m.orch, settings.RoleDev)
	choice, ok := o.selectedChoice()
	if !ok || choice.Engine != "legacy" || choice.Agent != "claude" {
		t.Fatalf("selected engine = %+v, ok=%v", choice, ok)
	}
}

func TestModelWorkspaceMouseChangesTask(t *testing.T) {
	m, _ := newTestModel(t)
	o := &taskModelOverlay{tasks: taskRoutes, sources: []modelSource{{id: "local", ready: true}}}
	o.viewSized(m.styles, 100, 28)
	o.originX, o.originY = 0, 0
	var target workspaceHit
	for _, hit := range o.hits {
		if hit.kind == "task" && hit.index == 2 {
			target = hit
			break
		}
	}
	o.mouse(m, tea.MouseMsg{X: target.x, Y: target.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if o.task != 2 || o.focus != 0 {
		t.Fatalf("mouse task selection = task %d focus %d", o.task, o.focus)
	}
}

func TestProvidersSlashOpensWorkspace(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 36})
	m.input.Set("/providers")
	feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlay != overlayProviders {
		t.Fatalf("overlay = %v, want providers", m.overlay)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "PROVIDER WORKSPACE") || !strings.Contains(view, "Codex · ChatGPT plan") {
		t.Fatalf("provider workspace missing from view: %q", view[:min(len(view), 900)])
	}
}

func TestProviderAndModelWorkspacesFitTerminal(t *testing.T) {
	for _, command := range []string{"/models", "/providers"} {
		for _, width := range []int{64, 96, 140} {
			m, _ := newTestModel(t)
			feed(m, tea.WindowSizeMsg{Width: width, Height: 32})
			m.input.Set(command)
			feed(m, tea.KeyMsg{Type: tea.KeyEnter})
			for row, line := range strings.Split(m.View(), "\n") {
				if got := lipgloss.Width(line); got > width {
					t.Fatalf("%s width %d row %d renders %d cells", command, width, row, got)
				}
			}
		}
	}
}

func TestTaskModelOverlayExposesKeyboardFocusWithoutColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m, _ := newTestModel(t)
	o := &taskModelOverlay{
		tasks: taskRoutes,
		sources: []modelSource{{
			id: "codex", label: "Codex · ChatGPT plan", ready: true, installed: true,
			models: []routeModel{{id: "gpt-5.6-luna"}},
		}},
	}

	checks := []struct {
		focus int
		label string
		mark  string
	}{
		{0, "FOCUS: TASK", "> CHAT"},
		{1, "FOCUS: PROVIDER", ">● Codex"},
		{2, "FOCUS: MODEL", ">gpt-5.6-luna"},
		{3, "FOCUS: REASONING", ">auto"},
	}
	for _, check := range checks {
		o.focus = check.focus
		view := stripANSI(o.viewSized(m.styles, 100, 28))
		if !strings.Contains(view, check.label) || !strings.Contains(view, check.mark) {
			t.Fatalf("focus %d is not textually visible; want %q and %q:\n%s", check.focus, check.label, check.mark, view)
		}
	}

	for _, focus := range []int{0, 1, 2, 3} {
		o.focus = focus
		view := stripANSI(o.viewSized(m.styles, 40, 10))
		if !strings.Contains(view, "FOCUS: "+[...]string{"TASK", "PROVIDER", "MODEL", "REASONING"}[focus]) {
			t.Fatalf("compact focus %d is not visible:\n%s", focus, view)
		}
	}
}

func TestTaskModelOverlayCompactRowsAreClickable(t *testing.T) {
	m, _ := newTestModel(t)
	o := &taskModelOverlay{
		tasks: taskRoutes,
		sources: []modelSource{{
			id: "codex", label: "Codex", ready: true, installed: true,
			models: []routeModel{{id: "gpt-5.6-luna"}},
		}},
	}
	o.viewSized(m.styles, 40, 10)
	if len(o.hits) != 4 {
		t.Fatalf("compact hit count = %d, want 4", len(o.hits))
	}
	for focus, kind := range []string{"task", "source", "model", "reasoning"} {
		o.focus = (focus + 1) % 4
		var hit workspaceHit
		for _, candidate := range o.hits {
			if candidate.kind == kind {
				hit = candidate
				break
			}
		}
		o.mouse(m, tea.MouseMsg{X: hit.x, Y: hit.y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
		if o.focus != focus {
			t.Fatalf("click %q focus = %d, want %d", kind, o.focus, focus)
		}
	}
}

func TestTaskModelOverlayReasoningFitsCompactAndWide(t *testing.T) {
	m, _ := newTestModel(t)
	o := &taskModelOverlay{
		tasks: taskRoutes,
		sources: []modelSource{{
			id: "openai", label: "OpenAI", kind: "native", ready: true, installed: true,
			models: []routeModel{{id: "openai/gpt-5.6-sol", reasoning: true, efforts: []string{"auto", "none", "low", "medium", "high", "xhigh", "max"}}},
		}},
		focus: 3,
	}
	for _, size := range []struct{ width, height int }{{40, 10}, {80, 28}, {240, 36}} {
		view := o.viewSized(m.styles, size.width, size.height)
		for row, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > size.width {
				t.Fatalf("%dx%d row %d width = %d", size.width, size.height, row, got)
			}
		}
		plain := stripANSI(view)
		if !strings.Contains(plain, "REASONING") && !strings.Contains(plain, "reasoning") {
			t.Fatalf("%dx%d omitted reasoning: %q", size.width, size.height, plain)
		}
	}
}
