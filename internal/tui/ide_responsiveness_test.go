package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/agentcore"
)

func TestIDEOpenDuringStreamingDoesNotWaitForFileScan(t *testing.T) {
	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	m.busy = true

	original := ideListFiles
	started := make(chan struct{})
	release := make(chan struct{})
	released := false
	ideListFiles = func(string, int) []string {
		close(started)
		<-release
		return []string{"ready.go"}
	}
	defer func() {
		ideListFiles = original
		if !released {
			close(release)
		}
	}()

	opened := make(chan tea.Cmd, 1)
	go func() { opened <- m.ToggleIDE() }()
	var refresh tea.Cmd
	select {
	case refresh = <-opened:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("opening the IDE blocked on workspace I/O")
	}
	if refresh == nil || m.ide == nil || !m.ide.filesLoading {
		t.Fatalf("deferred IDE state: cmd=%v ide=%v loading=%v", refresh != nil, m.ide != nil, m.ide != nil && m.ide.filesLoading)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "loading files") {
		t.Fatalf("deferred IDE did not render its loading state: %q", view)
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- refresh() }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background file scan did not start")
	}

	// The scan is deliberately stalled. Stream events and IDE rendering must
	// still progress on the event loop instead of queueing behind it.
	updated := make(chan struct{})
	go func() {
		feed(m, streamMsg{ev: agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "still responsive"})})
		_ = m.View()
		close(updated)
	}()
	select {
	case <-updated:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("stream update froze while the IDE workspace scan was running")
	}
	if got := m.LastAssistantText(); got != "still responsive" {
		t.Fatalf("stream text while IDE loading = %q", got)
	}

	close(release)
	released = true
	raw := <-result
	msg, ok := raw.(modFilesMsg)
	if !ok {
		t.Fatalf("background refresh returned %T", raw)
	}
	if next := m.finishModifiedFilesRefresh(msg); next != nil {
		t.Fatal("settled IDE refresh unexpectedly scheduled another scan")
	}
	if m.ide.filesLoading || !containsString(m.ide.files(), "ready.go") {
		t.Fatalf("background files were not applied: loading=%v files=%v", m.ide.filesLoading, m.ide.files())
	}
}

func TestStreamPumpBatchesBurstIntoOneFrame(t *testing.T) {
	m, _ := newTestModel(t)
	const count = 64
	for i := 0; i < count; i++ {
		m.events <- agentcore.NewEvent(nil, agentcore.RoleOrchestrator, agentcore.EvTextDelta, agentcore.TextDelta{Text: "x"})
	}

	raw := m.eventPump()()
	msg, ok := raw.(streamMsg)
	if !ok {
		t.Fatalf("event pump returned %T", raw)
	}
	if len(msg.events) != count {
		t.Fatalf("batched events = %d, want %d", len(msg.events), count)
	}
	feed(m, msg)
	if got := m.LastAssistantText(); got != strings.Repeat("x", count) {
		t.Fatalf("batched stream output = %q", got)
	}
}
