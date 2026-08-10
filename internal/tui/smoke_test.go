package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/proposals"
)

// TestFullStackRenderingExercises every new feature at once and asserts the
// screen stays intact: no broken ANSI, no width/height overflow, all
// premium surfaces present.
func TestFullStackRendering(t *testing.T) {
	m, dir := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 150, Height: 42})

	// Streaming turn with thinking, concealed code, grouped tools, links.
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "dev", Status: "running", Detail: "building",
	})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{
		Text: "See https://example.com/docs and a long block:\n\n```go\n" + strings.Repeat("line := 1\n", 20) + "```\n",
	})})
	for i := 0; i < 3; i++ {
		feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
			ID: "t" + string(rune('a'+i)), Name: "read", Output: "ok",
		})})
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "dev", Status: "done", Detail: "build finished",
	})})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvDone, agentcore.Done{})})

	// A proposed write card (staged through the model's own store so the
	// card resolves its proposal).
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "w1", Name: "write", Output: "staged p1 → target.txt",
	})})
	tool := proposals.StagingWriteTool(m.proposals)
	out, err := tool.Run(m.ctx(), map[string]any{"path": dir + "/target.txt", "content": "one\nTWO\n"})
	if err != nil || !strings.Contains(out, "staged") {
		t.Fatalf("staging tool: %v %q", err, out)
	}
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvToolResult, agentcore.ToolResult{
		ID: "w2", Name: "write", Output: out,
	})})
	m.renderMessages()

	view := m.View()
	lines := strings.Split(view, "\n")
	clean := stripANSI(view)
	for i, line := range lines {
		if brokenANSIRe.MatchString(line) {
			t.Fatalf("line %d broken ANSI: %q", i, stripANSI(line))
		}
		if w := lipgloss.Width(line); w > 150 {
			t.Fatalf("line %d width=%d > 150: %q", i, w, stripANSI(line))
		}
	}
	if got := lipgloss.Height(view); got > 42 {
		t.Fatalf("height=%d > 42", got)
	}
	for _, want := range []string{
		"worked", "3 × read", "[code · go", "https://example.com/docs",
	} {
		if !strings.Contains(clean, want) {
			t.Errorf("view missing %q", want)
		}
	}
	// Open the diff overlay on the proposed card.
	for _, msg := range m.messages {
		for _, c := range msg.Cards {
			if c.Status == "proposed" && c.Proposal != nil {
				m.overlay = overlayDiff
				m.overlayM = newDiffOverlay(m.styles, c.Proposal, 90)
			}
		}
	}
	view = m.View()
	clean = stripANSI(view)
	for _, want := range []string{"target.txt", "+", "−", "esc close"} {
		if !strings.Contains(clean, want) {
			t.Errorf("diff overlay missing %q", want)
		}
	}
	// Toggle the concealed block and re-check integrity.
	feed(m, tea.KeyMsg{Type: tea.KeyEsc}) // close the diff overlay
	m.focus = FocusViewport
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
	view = m.View()
	for i, line := range strings.Split(view, "\n") {
		if brokenANSIRe.MatchString(line) {
			t.Fatalf("line %d broken ANSI after toggle: %q", i, stripANSI(line))
		}
	}
	if !strings.Contains(stripANSI(view), "line := 1") {
		t.Error("expanded code block should show its body")
	}
}
