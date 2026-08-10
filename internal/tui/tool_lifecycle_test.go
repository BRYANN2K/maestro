package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestToolLifecycleResultAndTurnDoneAreTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})

	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvToolCall, agentcore.ToolCall{
		ID: "tool-1", Name: "command_execution",
	})})
	last := m.lastAssistant()
	if last == nil || len(last.Cards) != 1 || last.Cards[0].Status != "running" {
		t.Fatalf("started tool card = %+v", last)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Working…") {
		t.Fatalf("running tool did not render activity: %q", view)
	}

	// Successful commands are allowed to produce no stdout. EvToolResult is
	// terminal by contract; an empty Output must not be mistaken for a start.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "tool-1", Name: "command_execution", Output: "",
	})})
	if got := last.Cards[0].Status; got != "done" {
		t.Fatalf("empty successful tool result status = %q, want done", got)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})})
	if got := last.Cards[0].Status; got != "done" {
		t.Fatalf("tool status after turn done = %q, want done", got)
	}
	if len(m.toolCards) != 0 {
		t.Fatalf("terminal tool registry = %+v, want empty", m.toolCards)
	}
	if view := stripANSI(m.View()); strings.Contains(view, "Working…") || strings.Contains(view, "◌") {
		t.Fatalf("terminal transcript retained tool activity: %q", view)
	}
}

func TestTurnDoneClosesOrphanRunningTool(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolCall, agentcore.ToolCall{
		ID: "legacy-tool", Name: "/bin/zsh -lc true",
	})})

	// Some legacy providers only expose a completed subprocess envelope. The
	// end-of-turn event remains an authoritative lifecycle boundary.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvDone, agentcore.Done{})})
	last := m.lastAssistant()
	if last == nil || len(last.Cards) != 1 || last.Cards[0].Status != "done" {
		t.Fatalf("orphan tool was not terminalized: %+v", last)
	}
	if view := stripANSI(m.View()); strings.Contains(view, "Working…") || strings.Contains(view, "◌") {
		t.Fatalf("completed turn retained spinner: %q", view)
	}
}

func TestToolFailureIsTerminal(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolCall, agentcore.ToolCall{
		ID: "failed-tool", Name: "bash",
	})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "failed-tool", Name: "bash", Err: "exit status 1",
	})})
	if got := m.lastAssistant().Cards[0].Status; got != "error" {
		t.Fatalf("failed tool status = %q, want error", got)
	}
	if view := stripANSI(m.View()); strings.Contains(view, "Working…") {
		t.Fatalf("failed tool retained activity: %q", view)
	}
}
