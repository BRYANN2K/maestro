package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"
)

func TestWorkflowQueueFitsAndNavigatesSmallTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 80, Height: 24})
	w := &workflowOverlay{height: 18, snapshot: workflowSnapshot{Available: true, ChangeID: "sample", Phase: "draft"}}
	for i := 0; i < 50; i++ {
		w.snapshot.Tasks = append(w.snapshot.Tasks, workflowTask{ID: fmt.Sprintf("task-%02d", i)})
	}
	m.overlay, m.overlayM = overlayWorkflow, w
	for i := 0; i < 49; i++ {
		m.updateOverlayKey(tea.KeyMsg{Type: tea.KeyDown})
	}
	view := w.View(m.styles, 74)
	if w.selected != 49 || !strings.Contains(view, "task-49") || strings.Contains(view, "task-00") {
		t.Fatalf("queue navigation failed: %d\n%s", w.selected, view)
	}
	if lipgloss.Width(view) > 74 || lipgloss.Height(view) > 18 {
		t.Fatalf("overflow %dx%d\n%s", lipgloss.Width(view), lipgloss.Height(view), view)
	}
	if !strings.Contains(view, "esc back") {
		t.Fatal("footer clipped")
	}
}
