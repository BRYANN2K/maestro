package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/orchestrator"
)

func TestProvidersCommandKeepsUpdateViewAndResizeResponsive(t *testing.T) {
	original := providerCardsLoader
	started := make(chan struct{})
	release := make(chan struct{})
	providerCardsLoader = func(ctx context.Context, _ *orchestrator.Orchestrator) ([]providerCard, error) {
		close(started)
		select {
		case <-release:
			return []providerCard{{id: "ready", label: "Ready", connected: true}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer func() {
		providerCardsLoader = original
		close(release)
	}()

	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	m.input.Set("/providers")
	startedAt := time.Now()
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("/providers blocked Update for %s", elapsed)
	}
	if cmd == nil || m.overlay != overlayProviders || m.providersStop == nil {
		t.Fatalf("provider load was not scheduled: cmd=%v overlay=%v cancellable=%v", cmd != nil, m.overlay, m.providersStop != nil)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Loading accounts and model catalog") {
		t.Fatalf("provider loading state missing: %q", view)
	}

	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("provider worker did not start")
	}

	focus := m.focus
	startedAt = time.Now()
	m.Update(tea.WindowSizeMsg{Width: 96, Height: 26})
	_ = m.View()
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("resize/View waited for provider worker for %s", elapsed)
	}
	if m.focus != focus || m.width != 96 || m.height != 26 {
		t.Fatalf("provider worker disturbed focus/resize: focus=%v want=%v size=%dx%d", m.focus, focus, m.width, m.height)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != overlayNone || m.providersStop != nil {
		t.Fatalf("closing provider overlay did not cancel it: overlay=%v stop=%v", m.overlay, m.providersStop != nil)
	}
	select {
	case msg := <-result:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("cancelled provider worker did not finish")
	}
	if m.overlay != overlayNone {
		t.Fatal("cancelled provider result reopened its overlay")
	}
}

func TestProvidersNewestRequestWinsAndFailureIsVisible(t *testing.T) {
	original := providerCardsLoader
	call := 0
	providerCardsLoader = func(context.Context, *orchestrator.Orchestrator) ([]providerCard, error) {
		call++
		if call == 1 {
			return []providerCard{{id: "stale", label: "Stale"}}, nil
		}
		return nil, errors.New("status probe failed")
	}
	defer func() { providerCardsLoader = original }()

	m, _ := newTestModel(t)
	feed(m, tea.WindowSizeMsg{Width: 120, Height: 30})
	first := m.openProviders("stale")
	firstTarget := m.overlayM
	second := m.openProviders("current")
	secondTarget := m.overlayM
	if first == nil || second == nil || firstTarget == secondTarget {
		t.Fatal("reopening providers did not create a distinct request target")
	}

	m.Update(first())
	if m.overlayM != secondTarget || !secondTarget.(*providersOverlay).loading {
		t.Fatal("stale provider result replaced the current loading workspace")
	}
	m.Update(second())
	current := secondTarget.(*providersOverlay)
	if current.loading || current.loadErr == "" {
		t.Fatalf("current provider failure was not retained: loading=%v err=%q", current.loading, current.loadErr)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Provider status unavailable") || !strings.Contains(view, "r retry") {
		t.Fatalf("provider failure is not actionable: %q", view)
	}
}
