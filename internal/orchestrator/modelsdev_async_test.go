package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
)

func TestAsyncModelsDevStartupDoesNotWaitForNetworkAndCloseCancels(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	var startedOnce sync.Once
	var canceledOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-r.Context().Done()
		canceledOnce.Do(func() { close(canceled) })
	}))
	defer srv.Close()

	type result struct {
		orch *Orchestrator
		err  error
	}
	ready := make(chan result, 1)
	go func() {
		orch, err := New(context.Background(), Options{
			ProjectDir:  t.TempDir(),
			SessionsDir: filepath.Join(t.TempDir(), "sessions"),
			Config:      &config.Config{},
			In:          bytes.NewReader(nil),
			Out:         &bytes.Buffer{},
			ModelsDev: agentcore.NewModelsDev(agentcore.ModelsDevOptions{
				URL:          srv.URL,
				AsyncStartup: true,
			}),
		})
		ready <- result{orch: orch, err: err}
	}()

	var got result
	select {
	case got = <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("New blocked on the models.dev network request")
	}
	if got.err != nil {
		t.Fatalf("New: %v", got.err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		_ = got.orch.Close()
		t.Fatal("background models.dev refresh did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- got.orch.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not cancel the background models.dev request")
	}
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("models.dev handler did not observe cancellation")
	}
}

func TestAsyncModelsDevRefreshPublishesCatalog(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"fresh":{"id":"fresh","name":"Fresh","env":["FRESH_API_KEY"],"npm":"@ai-sdk/openai-compatible","api":%q,"models":{"fresh-model":{"id":"fresh-model","name":"Fresh Model","limit":{"context":77777,"output":4096},"status":"active"}}}}`, srvURL)
	}))
	defer srv.Close()
	srvURL = srv.URL

	orch, err := New(context.Background(), Options{
		ProjectDir:  t.TempDir(),
		SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config:      &config.Config{},
		In:          bytes.NewReader(nil),
		Out:         &bytes.Buffer{},
		ModelsDev: agentcore.NewModelsDev(agentcore.ModelsDevOptions{
			URL:          srv.URL,
			AsyncStartup: true,
		}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = orch.Close() }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		for _, model := range orch.ModelList(context.Background()) {
			if model.Provider == "fresh" && model.ID == "fresh-model" && model.Context == 77777 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("background models.dev catalog was not published")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAsyncModelsDevRefreshesPeriodicallyAndStopsOnClose(t *testing.T) {
	var hits atomic.Int32
	second := make(chan struct{})
	var secondOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) >= 2 {
			secondOnce.Do(func() { close(second) })
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	orch, err := New(context.Background(), Options{
		ProjectDir:  t.TempDir(),
		SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config:      &config.Config{},
		In:          bytes.NewReader(nil),
		Out:         &bytes.Buffer{},
		ModelsDev: agentcore.NewModelsDev(agentcore.ModelsDevOptions{
			URL: srv.URL, Refresh: 20 * time.Millisecond, AsyncStartup: true,
		}),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	select {
	case <-second:
	case <-time.After(2 * time.Second):
		_ = orch.Close()
		t.Fatalf("periodic refresh requests = %d, want at least 2", hits.Load())
	}
	if err := orch.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	stoppedAt := hits.Load()
	time.Sleep(80 * time.Millisecond)
	if got := hits.Load(); got != stoppedAt {
		t.Fatalf("refresh continued after Close: %d -> %d requests", stoppedAt, got)
	}
	if err := orch.RefreshModels(t.Context()); err == nil || !strings.Contains(err.Error(), "orchestrator is closed") {
		t.Fatalf("refresh after Close error = %v", err)
	}
}

func TestCloseCancelsManualModelRefreshWithoutWaitingForNetwork(t *testing.T) {
	started := make(chan struct{})
	handlerExited := make(chan struct{})
	var startOnce sync.Once
	var exitOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(started) })
		<-r.Context().Done()
		exitOnce.Do(func() { close(handlerExited) })
	}))
	defer srv.Close()

	orch, err := New(t.Context(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: &config.Config{}, In: bytes.NewReader(nil), Out: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.modelsDev = agentcore.NewModelsDev(agentcore.ModelsDevOptions{URL: srv.URL})
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- orch.RefreshModels(context.Background()) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("manual refresh did not start")
	}

	closeDone := make(chan error, 1)
	go func() { closeDone <- orch.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the manual model refresh")
	}
	select {
	case <-handlerExited:
	case <-time.After(time.Second):
		t.Fatal("manual refresh handler did not observe Close cancellation")
	}
	if err := <-refreshDone; err == nil {
		t.Fatal("cancelled RefreshModels unexpectedly succeeded")
	}
	if err := orch.RefreshModels(t.Context()); err == nil || !strings.Contains(err.Error(), "orchestrator is closed") {
		t.Fatalf("refresh after serialized Close error = %v", err)
	}
}

func TestNewestModelRefreshGenerationWinsOutOfOrderCompletion(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(releaseFirst) })
	var requests atomic.Int32
	var serverURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		if request == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-r.Context().Done():
				return
			}
		}
		model := "new-model"
		if request == 1 {
			model = "old-model"
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"remote":{"id":"remote","name":"Remote","api":%q,"models":{%q:{"id":%q,"name":%q}}}}`, serverURL, model, model, model)
	}))
	defer srv.Close()
	serverURL = srv.URL

	registry, err := agentcore.NewRegistry(t.Context(), &config.Config{}, nil, map[string]agentcore.CatalogProvider{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	orch := &Orchestrator{
		cfg:       &config.Config{},
		registry:  registry,
		modelsDev: agentcore.NewModelsDev(agentcore.ModelsDevOptions{URL: srv.URL}),
	}
	defer func() { _ = orch.Close() }()

	firstDone := make(chan error, 1)
	go func() { firstDone <- orch.RefreshModels(context.Background()) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}
	if err := orch.RefreshModels(t.Context()); err != nil {
		t.Fatalf("newest RefreshModels: %v", err)
	}
	releaseOnce.Do(func() { close(releaseFirst) })
	if err := <-firstDone; err != nil {
		t.Fatalf("superseded RefreshModels: %v", err)
	}

	catalog := registry.Catalog()
	if _, ok := catalog["remote"].Models["new-model"]; !ok {
		t.Fatalf("newest catalog was not retained: %#v", catalog["remote"].Models)
	}
	if _, ok := catalog["remote"].Models["old-model"]; ok {
		t.Fatal("stale refresh overwrote the newest catalog")
	}
}
