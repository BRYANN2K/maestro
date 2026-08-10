package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/git"
)

func TestTimelineJumpsToMessage(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.appendUser("first message")
	m.appendAssistant("second message")
	m.renderMessages()

	m.openTimeline()
	if m.overlay != overlayTimeline {
		t.Fatalf("timeline overlay = %v", m.overlay)
	}
	if !strings.Contains(stripANSI(m.View()), "first message") {
		t.Error("timeline should list messages")
	}
	// Jump to the first message.
	m.jumpToMessage(0)
	if m.overlay != overlayNone {
		t.Error("jump should close the timeline")
	}
	if m.forceFullRender {
		t.Error("forceFullRender must reset after jumping")
	}
}

func TestAgentDetailOverlay(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleDev, agentcore.EvSubAgent, agentcore.SubAgentStatus{
		Role: "dev", Status: "running", Detail: "compiling",
	})})
	m.overlay = overlayAgentDetail
	m.overlayM = &agentDetailOverlay{Role: "dev", Status: "running", Detail: "compiling"}
	view := stripANSI(m.View())
	if !strings.Contains(view, "compiling") || !strings.Contains(view, "cancel") {
		t.Errorf("agent detail missing: %q", view)
	}
	// "c" cancels the running agent.
	feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if m.overlay != overlayNone {
		t.Error("c should close the agent detail")
	}
}

func TestFocusGatesNotifications(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	feed(m, tea.BlurMsg{})
	if m.focused {
		t.Error("blur should clear focus")
	}
	feed(m, tea.FocusMsg{})
	if !m.focused {
		t.Error("focus should set focus")
	}
}

func TestStatuslinePowerlineChips(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 140, Height: 38})
	m.renderStatusline()
	view := m.status.View(m.styles, 140, m)
	if !strings.Contains(view, "Maestro is ready") {
		t.Errorf("statusline missing agent state: %q", stripANSI(view))
	}
	// Cost chip color thresholds.
	m.status.setSegs(statusSeg{Text: " $0.30 ", Color: m.styles.T.Color(TokenJulep)})
	view = m.status.View(m.styles, 140, m)
	if !strings.Contains(stripANSI(view), "$0.30") {
		t.Errorf("cost chip missing: %q", stripANSI(view))
	}
}

func TestSidebarChangedFiles(t *testing.T) {
	m, _ := newTestModel(t)
	styles := NewStyles(Charmtone())
	s := NewSidebar(Charmtone())
	s.width = 30
	view := stripANSI(s.View(styles, m.orch))
	if !strings.Contains(view, "CHANGED") || !strings.Contains(view, "none") {
		t.Errorf("empty changed section: %q", view)
	}
	s.setFiles([]git.NumStat{
		{Path: "src/main.go", Additions: 5, Removals: 2},
		{Path: "newfile.go", Additions: 3, Untracked: true},
		{Path: "gone.go", Removals: 4},
	})
	view = stripANSI(s.View(styles, m.orch))
	for _, want := range []string{"src/main.go", "+5", "-2", "newfile.go", "+3", "new", "gone.go", "-4"} {
		if !strings.Contains(view, want) {
			t.Errorf("changed view missing %q: %q", want, view)
		}
	}
}

func TestContextChip(t *testing.T) {
	cases := []struct {
		used, total int
		wantText    string
		wantRed     bool
	}{
		{50, 100, "ctx 50%", false},
		{60, 100, "ctx 60%", false},
		{79, 100, "ctx 79%", false},
		{80, 100, "⚠ctx 80%", true},
		{95, 100, "⚠ctx 95%", true},
		{150, 100, "⚠ctx 100%", true}, // clamped
		{0, 0, "", false},             // unknown window → hidden
	}
	for _, c := range cases {
		seg, ok := ctxChip(c.used, c.total, NewStyles(Charmtone()))
		if c.total <= 0 {
			if ok {
				t.Errorf("used=%d total=%d: chip should be hidden", c.used, c.total)
			}
			continue
		}
		if !ok {
			t.Errorf("used=%d total=%d: chip missing", c.used, c.total)
			continue
		}
		if !strings.Contains(stripANSI(seg.Text), c.wantText) {
			t.Errorf("used=%d total=%d: text %q, want %q", c.used, c.total, stripANSI(seg.Text), c.wantText)
		}
		if seg.Bold != c.wantRed {
			t.Errorf("used=%d total=%d: bold=%v, want %v", c.used, c.total, seg.Bold, c.wantRed)
		}
	}
}
