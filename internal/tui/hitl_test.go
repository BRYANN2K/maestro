package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestAgentRailHandlesPhysicalKeySpace(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.sidebar.setItem(agentcore.HITLItem{ID: "approval", Item: "Approve migration", Status: "pending"})
	m.focus = FocusSidebar

	feed(m, tea.KeyMsg{Type: tea.KeySpace})

	if !m.sidebar.checked["approval"] {
		t.Fatal("physical KeySpace did not toggle the focused Agent rail action")
	}
}

func TestSyntheticHITLSummaryIsIgnored(t *testing.T) {
	m, _ := newTestModel(t)
	m.sidebar.setItem(agentcore.HITLItem{ID: "hitl", Item: "Complete the HITL items below", Status: "pending"})
	m.sidebar.setItem(agentcore.HITLItem{ID: "approval", Item: "Approve migration", Status: "pending"})
	completed, total, blocking := m.sidebar.hitlProgress()
	if completed != 0 || total != 1 || blocking != 0 {
		t.Fatalf("progress = %d/%d blocking=%d, want 0/1 blocking=0", completed, total, blocking)
	}
	view := stripANSI(m.sidebar.View(m.styles, m.orch))
	if strings.Contains(view, "0/2") || strings.Contains(view, "Complete the HITL") {
		t.Fatalf("synthetic summary rendered as an action:\n%s", view)
	}
	if strings.Contains(view, "PAUSED") || !strings.Contains(view, "ACTIONS · 1 to review") {
		t.Fatalf("non-blocking action has incoherent gate status:\n%s", view)
	}
}

func TestBlockingHITLAloneShowsPaused(t *testing.T) {
	m, _ := newTestModel(t)
	m.sidebar.setItem(agentcore.HITLItem{ID: "env:DATABASE_URL", Item: "Fill .env with DATABASE_URL", Status: "pending", Blocking: true})
	view := stripANSI(m.sidebar.View(m.styles, m.orch))
	if !strings.Contains(view, "HUMAN ACTIONS  0/1") || !strings.Contains(view, "PAUSED · 1 blocking") {
		t.Fatalf("blocking action status missing:\n%s", view)
	}
}
