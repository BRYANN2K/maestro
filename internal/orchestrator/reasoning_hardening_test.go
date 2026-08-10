package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/settings"
)

func TestMaestrorcReasoningFlowsConfigToOpenAIWire(t *testing.T) {
	requests := make(chan map[string]any, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	cfg := config.New()
	cfg.Providers = []config.Provider{{Name: "custom-openai", Type: "openai", BaseURL: srv.URL}}
	cfg.Models = []config.Model{{ID: "custom-openai/reasoner", CanReason: true}}
	cfg.ModelRoles["default"] = config.Slot{
		Model:    "custom-openai/reasoner",
		Sampling: config.Sampling{ReasoningEffort: "high", MaxTokens: 3210},
	}
	newOrchestrator := func(t *testing.T, state settings.Settings) *Orchestrator {
		t.Helper()
		dir := t.TempDir()
		o, err := New(t.Context(), Options{
			ProjectDir: dir, SessionsDir: filepath.Join(dir, "sessions"),
			In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
			Keys: mapKeyStore{"custom-openai": "test"}, Settings: state,
		})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}

	o := newOrchestrator(t, settings.Defaults())
	if err := o.Chat(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	body := <-requests
	if body["reasoning_effort"] != "high" || body["max_tokens"] != float64(3210) {
		t.Fatalf("config sampling was dropped: %#v", body)
	}
	if got := o.ActiveReasoningEffort(); got != "high" {
		t.Fatalf("status/runtime split: ActiveReasoningEffort = %q", got)
	}
	next := o.SettingsSnapshot()
	route := next.RoleDefaults[settings.RoleOrchestrator]
	route.ReasoningEffort, route.ReasoningSet = "low", true
	next.RoleDefaults[settings.RoleOrchestrator] = route
	if err := o.UpdateSettings(t.Context(), next); err != nil {
		t.Fatalf("explicit effort on inherited config model: %v", err)
	}
	if got := o.ActiveReasoningEffort(); got != "low" {
		t.Fatalf("Settings/runtime split after update: %q", got)
	}

	state := settings.Defaults()
	route = state.RoleDefaults[settings.RoleOrchestrator]
	route.ReasoningSet = true // explicit automatic overrides maestrorc high
	state.RoleDefaults[settings.RoleOrchestrator] = route
	o = newOrchestrator(t, state)
	if err := o.Chat(t.Context(), "hello again"); err != nil {
		t.Fatal(err)
	}
	body = <-requests
	if _, exists := body["reasoning_effort"]; exists {
		t.Fatalf("explicit Settings auto did not override config: %#v", body)
	}
}

func TestReasoningCapabilitiesUseActualProviderType(t *testing.T) {
	cfg := config.New()
	cfg.Providers = []config.Provider{
		{Name: "not-named-anthropic", Type: "anthropic", BaseURL: "https://example.invalid"},
		{Name: "local-reasoner", Type: "ollama", BaseURL: "http://127.0.0.1:11434/v1"},
	}
	cfg.Models = []config.Model{
		{ID: "not-named-anthropic/claude-sonnet-4-6", CanReason: true},
		{ID: "local-reasoner/r1", CanReason: true},
	}
	o, err := New(t.Context(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg, Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(o.ReasoningEfforts("native", "", "not-named-anthropic/claude-sonnet-4-6"), ","); got != "auto,low,medium,high,max" {
		t.Fatalf("custom Anthropic efforts = %q", got)
	}
	if got := strings.Join(o.ReasoningEfforts("native", "", "local-reasoner/r1"), ","); got != "auto" {
		t.Fatalf("generic CanReason advertised ignored values: %q", got)
	}
}

func TestStartupMigratesIncompatiblePersistedReasoning(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	state := settings.Defaults()
	state.RoleDefaults[settings.RoleOrchestrator] = settings.RoleDefaults{
		Engine: "native", Model: "custom/gpt-4.1",
		ReasoningEffort: "high", ReasoningSet: true,
	}
	if err := state.Save(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	cfg := config.New()
	cfg.Providers = []config.Provider{{Name: "custom", Type: "openai", BaseURL: "https://example.invalid/v1"}}
	cfg.Models = []config.Model{{ID: "custom/gpt-4.1"}}
	if _, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(dir, "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
		Settings: state, SettingsPath: path,
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := settings.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	route := loaded.RoleDefaults[settings.RoleOrchestrator]
	if route.ReasoningEffort != "" || route.ReasoningSet {
		t.Fatalf("incompatible persisted effort was not migrated: %+v", route)
	}
}

func TestStartupRejectsIncompatibleMaestrorcReasoning(t *testing.T) {
	cfg := config.New()
	cfg.Providers = []config.Provider{{Name: "custom", Type: "openai", BaseURL: "https://example.invalid/v1"}}
	cfg.Models = []config.Model{{ID: "custom/gpt-4.1"}}
	cfg.ModelRoles["default"] = config.Slot{Model: "custom/gpt-4.1", Sampling: config.Sampling{ReasoningEffort: "high"}}
	_, err := New(t.Context(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg, Settings: settings.Defaults(),
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("New error = %v, want incompatible reasoning failure", err)
	}
}

func TestUpdateSettingsRollsBackOnSaveFailureAndDuringRun(t *testing.T) {
	dir := t.TempDir()
	parentFile := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	o, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(dir, "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Settings: settings.Defaults(),
		SettingsPath: filepath.Join(parentFile, "settings.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	before := o.SettingsSnapshot()
	next := o.SettingsSnapshot()
	next.Theme = "mono"
	if err := o.UpdateSettings(t.Context(), next); err == nil {
		t.Fatal("UpdateSettings succeeded despite unwritable settings path")
	}
	if got := o.SettingsSnapshot().Theme; got != before.Theme {
		t.Fatalf("runtime settings changed after failed Save: %q", got)
	}

	runCtx, cancel := o.withRunContext(context.Background())
	defer cancel()
	if runCtx.Err() != nil {
		t.Fatal(runCtx.Err())
	}
	if err := o.UpdateSettings(t.Context(), next); err == nil || !strings.Contains(err.Error(), "run is active") {
		t.Fatalf("active-run update error = %v", err)
	}
	cancel()
	if err := o.UpdateSettings(t.Context(), next); err == nil || strings.Contains(err.Error(), "run is active") {
		t.Fatalf("run remained active after cleanup: %v", err)
	}
}

func TestSetActiveModelPinsEffectiveOrchestratorRoute(t *testing.T) {
	cfg := config.New()
	cfg.Providers = []config.Provider{{Name: "custom", Type: "openai", BaseURL: "https://example.invalid/v1"}}
	cfg.Models = []config.Model{{ID: "custom/reasoner", CanReason: true}}
	o, err := New(t.Context(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg, Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SetActiveModel(t.Context(), "custom/reasoner"); err != nil {
		t.Fatal(err)
	}
	route := o.SettingsSnapshot().RoleDefaults[settings.RoleOrchestrator]
	if route.Engine != "native" || route.Model != "custom/reasoner" || o.ActiveModel() != route.Model {
		t.Fatalf("route/model split: route=%+v active=%q", route, o.ActiveModel())
	}
}
