package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAtFilePickerKeepsUpdateViewAndResizeResponsive(t *testing.T) {
	original := atFileListLoader
	started := make(chan struct{})
	release := make(chan struct{})
	atFileListLoader = func(ctx context.Context, _ string, _ int) ([]string, error) {
		close(started)
		select {
		case <-release:
			return []string{"main.go"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer func() {
		atFileListLoader = original
		close(release)
	}()

	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	startedAt := time.Now()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("@file blocked Update for %s", elapsed)
	}
	if cmd == nil || m.overlay != overlayAtFile || !m.atFileLoading || m.atFileStop == nil {
		t.Fatalf("@file load was not scheduled: cmd=%v overlay=%v loading=%v cancellable=%v", cmd != nil, m.overlay, m.atFileLoading, m.atFileStop != nil)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Loading files for @") {
		t.Fatalf("@file loading state missing: %q", view)
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("@file worker did not start")
	}

	focus := m.focus
	startedAt = time.Now()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 26})
	_ = m.View()
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("@file query/resize/View waited for listing for %s", elapsed)
	}
	if m.input.Value() != "@m" || m.focus != focus || m.width != 96 || m.height != 26 {
		t.Fatalf("@file async state drift: input=%q focus=%v want=%v size=%dx%d", m.input.Value(), m.focus, focus, m.width, m.height)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != overlayNone || m.atFileStop != nil || m.atFileLoading {
		t.Fatalf("closing @file did not cancel it: overlay=%v stop=%v loading=%v", m.overlay, m.atFileStop != nil, m.atFileLoading)
	}
	select {
	case msg := <-result:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("cancelled @file worker did not finish")
	}
	if m.overlay != overlayNone {
		t.Fatal("cancelled @file result reopened its picker")
	}
}

func TestAtFileFailureIsVisibleAndRetryable(t *testing.T) {
	original := atFileListLoader
	atFileListLoader = func(context.Context, string, int) ([]string, error) {
		return nil, errors.New("listing failed")
	}
	defer func() { atFileListLoader = original }()

	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.input.Set("@main")
	cmd := m.maybeOpenAtFile()
	if cmd == nil {
		t.Fatal("@file failure path did not schedule a worker")
	}
	m.Update(cmd())
	if m.atFileLoading || m.atFileError == "" {
		t.Fatalf("@file failure state: loading=%v err=%q", m.atFileLoading, m.atFileError)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Files unavailable") || !strings.Contains(view, "ctrl+r retry") {
		t.Fatalf("@file failure is not actionable: %q", view)
	}
}

func TestCtrlQImmediatelyCancelsBusyRunAndAsyncPicker(t *testing.T) {
	original := atFileListLoader
	listingStarted := make(chan struct{})
	atFileListLoader = func(ctx context.Context, _ string, _ int) ([]string, error) {
		close(listingStarted)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	defer func() { atFileListLoader = original }()

	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	runCtx, cancelRun := context.WithCancel(context.Background())
	m.busy = true
	m.cancelRun = cancelRun
	runDone := make(chan struct{})
	go func() {
		<-runCtx.Done()
		close(runDone)
	}()
	modifiedCtx, cancelModified := context.WithCancel(context.Background())
	m.modFilesStop = cancelModified
	m.modFilesInFlight = true
	m.modFilesRunning = 7
	modifiedDone := make(chan struct{})
	go func() {
		<-modifiedCtx.Done()
		close(modifiedDone)
	}()

	m.input.Set("@")
	listCmd := m.maybeOpenAtFile()
	listDone := make(chan tea.Msg, 1)
	go func() { listDone <- listCmd() }()
	select {
	case <-listingStarted:
	case <-time.After(time.Second):
		t.Fatal("@file worker did not start")
	}

	startedAt := time.Now()
	_, quit := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("ctrl+q waited for workers for %s", elapsed)
	}
	if quit == nil || m.atFileStop != nil || m.modFilesStop != nil || m.modFilesInFlight {
		t.Fatalf("ctrl+q did not prepare shutdown: quit=%v atFileStop=%v modFilesStop=%v modFilesInFlight=%v", quit != nil, m.atFileStop != nil, m.modFilesStop != nil, m.modFilesInFlight)
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("ctrl+q leaked the active run")
	}
	select {
	case <-listDone:
	case <-time.After(time.Second):
		t.Fatal("ctrl+q leaked the @file worker")
	}
	select {
	case <-modifiedDone:
	case <-time.After(time.Second):
		t.Fatal("ctrl+q leaked the modified-files worker")
	}
}
