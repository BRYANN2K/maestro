package tui

import (
	"context"
	"errors"
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
	ideListFiles = func(ctx context.Context, _ string, _ int) ([]string, error) {
		close(started)
		select {
		case <-release:
			return []string{"ready.go"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
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
	refresh = finishIDEHydrationForTest(t, m, refresh)
	if refresh == nil {
		t.Fatal("IDE hydration did not schedule its workspace file list")
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

func TestIDEFileListFailureStopsLoadingAndRetries(t *testing.T) {
	original := ideListFiles
	calls := 0
	ideListFiles = func(context.Context, string, int) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("listing failed")
		}
		return []string{"ready.go"}, nil
	}
	defer func() { ideListFiles = original }()

	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 160, Height: 36})
	hydration := m.ToggleIDE()
	if hydration == nil || m.ide == nil {
		t.Fatal("IDE did not schedule its deferred hydration")
	}
	initial := finishIDEHydrationForTest(t, m, hydration)
	if initial == nil {
		t.Fatal("IDE did not schedule its file listing")
	}
	m.ide.fileCache = []string{"cached.go"}
	feed(m, initial())
	if m.ide.filesLoading {
		t.Fatalf("failed file listing left the IDE loading forever: calls=%d requested=%d applied=%d running=%d inFlight=%v error=%q", calls, m.modFilesRequested, m.modFilesApplied, m.modFilesRunning, m.modFilesInFlight, m.ideFilesError)
	}
	if m.ideFilesError != "listing failed" || m.ide.Ed == nil || !strings.Contains(m.ide.Ed.Status, "ctrl+r retry") {
		t.Fatalf("file listing error is not actionable: error=%q status=%q", m.ideFilesError, m.ide.Ed.Status)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "ctrl+r retry") {
		t.Fatalf("file listing error/retry is not visible in the IDE:\n%s", view)
	}
	if !containsString(m.ide.files(), "cached.go") {
		t.Fatalf("failed refresh replaced the last valid cache: %v", m.ide.files())
	}

	_, retry := m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if retry == nil || !m.ide.filesLoading || m.ideFilesError != "" {
		t.Fatalf("ctrl+r did not start a clean retry: cmd=%v loading=%v error=%q", retry != nil, m.ide.filesLoading, m.ideFilesError)
	}
	feed(m, retry())
	if m.ide.filesLoading || m.ideFilesError != "" || !containsString(m.ide.files(), "ready.go") {
		t.Fatalf("successful retry did not install the file snapshot: loading=%v error=%q files=%v", m.ide.filesLoading, m.ideFilesError, m.ide.files())
	}
	if strings.HasPrefix(m.ide.Ed.Status, ideFileListStatus) || m.ide.Ed.Status == ideFileListLoading {
		t.Fatalf("successful retry left a stale error status: %q", m.ide.Ed.Status)
	}
	feed(m, modFilesMsg{
		revision:    m.modFilesApplied - 1,
		workspace:   m.orch.SnapshotWorkspace(),
		ideFilesErr: errors.New("late stale failure"),
	})
	if m.ideFilesError != "" || m.ide.filesLoading || !containsString(m.ide.files(), "ready.go") {
		t.Fatalf("stale failure corrupted the latest snapshot: loading=%v error=%q files=%v", m.ide.filesLoading, m.ideFilesError, m.ide.files())
	}
}

func TestApplyLoadedSessionSchedulesIDEHydration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	m, _ := newTestModel(t)
	if m.ToggleIDE() == nil || m.ide == nil {
		t.Fatal("IDE did not open")
	}
	previous := m.ide
	hydration := m.applyLoadedSession(m.orch.Session().ID, m.orch.WorkDirDisplay())
	if hydration == nil || m.ide == nil || m.ide == previous || !m.ide.hydrating {
		t.Fatalf("loaded session left an unhydrated IDE: cmd=%v ide=%v replaced=%v hydrating=%v", hydration != nil, m.ide != nil, m.ide != previous, m.ide != nil && m.ide.hydrating)
	}
	msg, ok := hydration().(ideOperationMsg)
	if !ok || msg.kind != ideOperationHydrate || msg.target != m.ide {
		t.Fatalf("session hydration command = %T %+v", msg, msg)
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

func TestCtrlQCancelsModifiedFilesListing(t *testing.T) {
	original := ideListFiles
	started := make(chan struct{})
	ideListFiles = func(ctx context.Context, _ string, _ int) ([]string, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	defer func() { ideListFiles = original }()

	m, _ := newTestModel(t)
	m.modFilesRequested = 1
	cmd := m.beginModifiedFilesRefresh()
	if cmd == nil {
		t.Fatal("modified-files refresh was not scheduled")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("modified-files listing did not start")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("ctrl+q did not cancel the modified-files listing")
	}
	if m.modFilesStop != nil || m.modFilesInFlight {
		t.Fatalf("modified-files worker remained active: stop=%v inFlight=%v", m.modFilesStop != nil, m.modFilesInFlight)
	}
}
