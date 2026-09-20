package tui

import (
	"context"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bryann2k/maestro/internal/orchestrator"
)

func TestModelRefreshEscapeCancelsAndStaleResultCannotReopenPicker(t *testing.T) {
	original := modelCatalogRefresher
	started := make(chan struct{})
	modelCatalogRefresher = func(ctx context.Context, _ *orchestrator.Orchestrator) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	defer func() { modelCatalogRefresher = original }()

	m, _ := newTestModel(t)
	target := &taskModelOverlay{tasks: append([]taskRoute(nil), taskRoutes...), focus: 2}
	m.overlay = overlayModelPicker
	m.overlayM = target
	cmd := target.update(m, tea.KeyMsg{Type: tea.KeyCtrlR})
	if cmd == nil || m.modelRefreshStop == nil {
		t.Fatal("ctrl+r did not start a cancellable model refresh")
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model refresh worker did not start")
	}

	target.update(m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.overlay != overlayNone || m.modelRefreshStop != nil {
		t.Fatalf("escape did not close and cancel refresh: overlay=%v stop=%v", m.overlay, m.modelRefreshStop != nil)
	}
	toasts := len(m.status.toasts)
	select {
	case msg := <-result:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("cancelled model refresh did not finish")
	}
	if m.overlay != overlayNone || len(m.status.toasts) != toasts {
		t.Fatalf("stale refresh changed closed UI: overlay=%v toasts=%d->%d", m.overlay, toasts, len(m.status.toasts))
	}
}

func TestCtrlQCancelsModelRefreshWorker(t *testing.T) {
	original := modelCatalogRefresher
	started := make(chan struct{})
	modelCatalogRefresher = func(ctx context.Context, _ *orchestrator.Orchestrator) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	defer func() { modelCatalogRefresher = original }()

	m, _ := newTestModel(t)
	m.overlay = overlayProviders
	m.overlayM = newLoadingProvidersOverlay("")
	cmd := m.refreshModelsForOverlay()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("model refresh worker did not start")
	}

	_, quit := m.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	if quit == nil || m.modelRefreshStop != nil {
		t.Fatalf("ctrl+q shutdown did not cancel model refresh: quit=%v stop=%v", quit != nil, m.modelRefreshStop != nil)
	}
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("ctrl+q leaked the model refresh worker")
	}
}

func TestFirstLocalModelDiscoveryUpdatesOpenPickerInPlace(t *testing.T) {
	originalLoad := taskModelOverlayLoader
	originalDiscovery := taskModelDiscoveryLoader
	discoveryStarted := make(chan struct{})
	seenBaseline := make(chan string, 1)
	releaseDiscovery := make(chan struct{})
	taskModelOverlayLoader = func(context.Context, *orchestrator.Orchestrator) (*taskModelOverlay, error) {
		return &taskModelOverlay{
			tasks: append([]taskRoute(nil), taskRoutes...),
			sources: []modelSource{{
				id: "local", label: "Local", kind: "native", ready: true, installed: true,
			}},
			focus: 2, discoveryPending: true, discoveryBaseline: "empty",
		}, nil
	}
	taskModelDiscoveryLoader = func(ctx context.Context, _ *orchestrator.Orchestrator, baseline string) (*taskModelOverlay, error) {
		seenBaseline <- baseline
		close(discoveryStarted)
		select {
		case <-releaseDiscovery:
			return &taskModelOverlay{
				tasks: append([]taskRoute(nil), taskRoutes...),
				sources: []modelSource{{
					id: "local", label: "Local", kind: "native", ready: true, installed: true,
					models: []routeModel{{id: "local/live-model", name: "Live model"}},
				}},
				focus: 2,
			}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	defer func() {
		taskModelOverlayLoader = originalLoad
		taskModelDiscoveryLoader = originalDiscovery
	}()

	m, _ := newTestModel(t)
	initial := m.openModelPicker()
	target := m.overlayM.(*taskModelOverlay)
	initialMsg := initial()
	_, follow := m.Update(initialMsg)
	if m.overlayM != target || target.loading || !target.discoveryPending {
		t.Fatalf("initial local picker state was not retained: same=%v loading=%v pending=%v", m.overlayM == target, target.loading, target.discoveryPending)
	}
	if follow == nil {
		t.Fatal("initial local picker did not schedule its bounded discovery notification")
	}
	result := make(chan tea.Msg, 1)
	var runCmd func(tea.Cmd)
	runCmd = func(cmd tea.Cmd) {
		if cmd == nil {
			return
		}
		raw := cmd()
		if batch, ok := raw.(tea.BatchMsg); ok {
			for _, child := range batch {
				go runCmd(child)
			}
			return
		}
		result <- raw
	}
	go runCmd(follow)
	select {
	case <-discoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("local model discovery watcher did not start")
	}
	if baseline := <-seenBaseline; baseline != "empty" {
		t.Fatalf("discovery baseline = %q, want empty", baseline)
	}
	close(releaseDiscovery)
	select {
	case msg := <-result:
		m.Update(msg)
	case <-time.After(time.Second):
		t.Fatal("local model discovery notification did not arrive")
	}

	if m.overlay != overlayModelPicker || m.overlayM != target {
		t.Fatal("local discovery reopened or replaced the active picker")
	}
	if got := target.sources[0].models; len(got) != 1 || got[0].id != "local/live-model" {
		t.Fatalf("discovered local models = %#v", got)
	}
	if target.discoveryPending || m.modelPickerStop != nil {
		t.Fatalf("discovery did not settle: pending=%v stop=%v", target.discoveryPending, m.modelPickerStop != nil)
	}
}
