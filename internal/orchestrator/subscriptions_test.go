package orchestrator

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/settings"
)

func TestSetTaskModelPersistsRoleRoute(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	o, err := New(context.Background(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(dir, "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: config.New(),
		Settings: settings.Defaults(), SettingsPath: settingsPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := o.SetTaskModel(context.Background(), settings.RoleDev, "legacy", "claude", "sonnet"); err != nil {
		t.Fatalf("SetTaskModel: %v", err)
	}
	loaded, err := settings.Load(context.Background(), settingsPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := settings.RoleDefaults{Engine: "legacy", Agent: "claude", Model: "sonnet"}
	if got := loaded.RoleDefaults[settings.RoleDev]; got != want {
		t.Fatalf("persisted route = %+v, want %+v", got, want)
	}
}

func TestReasoningCapabilitiesMatchRoute(t *testing.T) {
	cfg := config.New()
	cfg.Providers = []config.Provider{{Name: "openai", Type: "openai-compat", BaseURL: "https://api.openai.com/v1"}}
	cfg.Models = []config.Model{
		{ID: "openai/gpt-5.6-sol", CanReason: true},
		{ID: "openai/o3", CanReason: true},
		{ID: "openai/gpt-4.1"},
	}
	o, err := New(t.Context(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
		Keys: mapKeyStore{"openai": "test"}, Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		engine, agent, model string
		want                 string
	}{
		{"legacy", "codex", "gpt-5.6-sol", "auto,minimal,low,medium,high,xhigh"},
		{"legacy", "claude", "sonnet", "auto"},
		{"native", "", "openai/gpt-5.6-sol", "auto,none,low,medium,high,xhigh,max"},
		{"native", "", "openai/o3", "auto,low,medium,high"},
		{"native", "", "openai/gpt-4.1", "auto"},
	}
	for _, tt := range tests {
		if got := strings.Join(o.ReasoningEfforts(tt.engine, tt.agent, tt.model), ","); got != tt.want {
			t.Errorf("ReasoningEfforts(%s,%s,%s) = %q, want %q", tt.engine, tt.agent, tt.model, got, tt.want)
		}
	}
}

func TestSetTaskModelWithReasoningPersistsAndResetsWhenIncompatible(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	o, err := New(t.Context(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(dir, "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: config.New(),
		Settings: settings.Defaults(), SettingsPath: settingsPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SetTaskModelWithReasoning(t.Context(), settings.RoleDev, "legacy", "codex", "gpt-5.6-sol", "xhigh"); err != nil {
		t.Fatal(err)
	}
	if got := o.SettingsSnapshot().RoleDefaults[settings.RoleDev].ReasoningEffort; got != "xhigh" {
		t.Fatalf("reasoning effort = %q", got)
	}
	if err := o.SetTaskModel(t.Context(), settings.RoleDev, "legacy", "claude", "sonnet"); err != nil {
		t.Fatal(err)
	}
	if got := o.SettingsSnapshot().RoleDefaults[settings.RoleDev].ReasoningEffort; got != "" {
		t.Fatalf("incompatible reasoning was not reset: %q", got)
	}
	loaded, err := settings.Load(t.Context(), settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.RoleDefaults[settings.RoleDev].ReasoningEffort; got != "" {
		t.Fatalf("persisted reset = %q", got)
	}
}

func TestActiveReasoningEffortReportsEffectiveSelection(t *testing.T) {
	o, err := New(t.Context(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SetTaskModelWithReasoning(t.Context(), settings.RoleOrchestrator, "legacy", "codex", "gpt-5.6-sol", "high"); err != nil {
		t.Fatal(err)
	}
	if got := o.ActiveReasoningEffort(); got != "high" {
		t.Fatalf("ActiveReasoningEffort = %q", got)
	}
	if err := o.SetTaskModel(t.Context(), settings.RoleOrchestrator, "legacy", "claude", "sonnet"); err != nil {
		t.Fatal(err)
	}
	if got := o.ActiveReasoningEffort(); got != "auto" {
		t.Fatalf("incompatible effective effort = %q", got)
	}
}

func TestRunnerReasoningParity(t *testing.T) {
	cfg := config.New()
	cfg.Providers = []config.Provider{{Name: "openai", Type: "openai-compat", BaseURL: "https://api.openai.com/v1"}}
	cfg.Models = []config.Model{{ID: "openai/o3", CanReason: true}}
	o, err := New(t.Context(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
		Keys: mapKeyStore{"openai": "test"}, Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatal(err)
	}
	o.settings.RoleDefaults[settings.RoleDev] = settings.RoleDefaults{Engine: "legacy", Agent: "codex", ReasoningEffort: "high"}
	runner, err := o.buildRunner(BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.(*legacyRunner).reasoningEffort; got != "high" {
		t.Fatalf("legacy reasoning = %q", got)
	}
	o.settings.RoleDefaults[settings.RoleDev] = settings.RoleDefaults{Engine: "native", Model: "openai/o3", ReasoningEffort: "high"}
	runner, err = o.buildRunner(BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.(*nativeRunner).reasoningEffort; got != "high" {
		t.Fatalf("native reasoning = %q", got)
	}
}

func TestSetTaskModelAcceptsSubscriptionProductName(t *testing.T) {
	o, err := New(context.Background(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: config.New(), Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := o.SetTaskModel(context.Background(), settings.RoleDev, "subscription", "codex", "gpt-5.6-luna"); err != nil {
		t.Fatalf("SetTaskModel subscription: %v", err)
	}
	want := settings.RoleDefaults{Engine: "legacy", Agent: "codex", Model: "gpt-5.6-luna"}
	if got := o.SettingsSnapshot().RoleDefaults[settings.RoleDev]; got != want {
		t.Fatalf("normalized subscription route = %+v, want %+v", got, want)
	}
}

func TestSetTaskModelNormalizesAuto(t *testing.T) {
	o, err := New(context.Background(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: config.New(), Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := o.SetTaskModel(context.Background(), settings.RoleOrchestrator, "legacy", "codex", "auto"); err != nil {
		t.Fatalf("SetTaskModel: %v", err)
	}
	if got := o.SettingsSnapshot().RoleDefaults[settings.RoleOrchestrator].Model; got != "" {
		t.Fatalf("auto model persisted as %q", got)
	}
}

func TestPersistedLegacyRouteWinsNativeConfigAfterRestart(t *testing.T) {
	dir := t.TempDir()
	settingsPath := filepath.Join(dir, "settings.json")
	wantRoute := settings.RoleDefaults{Engine: "legacy", Agent: "codex", Model: "gpt-5.6-luna"}
	st := settings.Defaults()
	st.RoleDefaults[settings.RoleOrchestrator] = wantRoute
	if err := st.Save(context.Background(), settingsPath); err != nil {
		t.Fatalf("Save settings: %v", err)
	}

	restored, err := settings.Load(context.Background(), settingsPath)
	if err != nil {
		t.Fatalf("Load settings: %v", err)
	}
	cfg := config.New()
	cfg.ModelRoles["default"] = config.Slot{Model: "native/config-default"}
	cfg.Models = []config.Model{
		{ID: "native/config-default", ContextWindow: 64_000},
		{ID: "gpt-5.6-luna", ContextWindow: 1_050_000},
	}
	o, err := New(context.Background(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(dir, "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
		Settings: restored, SettingsPath: settingsPath,
	})
	if err != nil {
		t.Fatalf("New after restart: %v", err)
	}

	if got := o.defaultModel(); got != "native/config-default" {
		t.Fatalf("native config fallback = %q, want native/config-default", got)
	}
	if got := o.effectiveRoleRoute(settings.RoleOrchestrator); got != wantRoute {
		t.Fatalf("effective route = %+v, want %+v", got, wantRoute)
	}
	if got := o.ActiveModel(); got != wantRoute.Model {
		t.Fatalf("ActiveModel = %q, want %q", got, wantRoute.Model)
	}
	runner, err := o.runnerForRole(settings.RoleOrchestrator)
	if err != nil {
		t.Fatalf("runnerForRole: %v", err)
	}
	legacy, ok := runner.(*legacyRunner)
	if !ok {
		t.Fatalf("runner = %T, want *legacyRunner", runner)
	}
	if legacy.model != o.ActiveModel() {
		t.Fatalf("runner model = %q, ActiveModel = %q", legacy.model, o.ActiveModel())
	}
	if _, total := o.ContextUsage(); total != 1_050_000 {
		t.Fatalf("usage context = %d, want Luna context 1050000", total)
	}
}

func TestEffectiveRoleRoutePreservesNativeFallbackAndLegacyAuto(t *testing.T) {
	cfg := config.New()
	cfg.ModelRoles["default"] = config.Slot{Model: "native/config-default"}
	o, err := New(context.Background(), Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
		Settings: settings.Defaults(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := o.ActiveModel(); got != "native/config-default" {
		t.Fatalf("native ActiveModel = %q, want config fallback", got)
	}
	runner, err := o.runnerForRole(settings.RoleOrchestrator)
	if err != nil {
		t.Fatalf("native runnerForRole: %v", err)
	}
	native, ok := runner.(*nativeRunner)
	if !ok {
		t.Fatalf("runner = %T, want *nativeRunner", runner)
	}
	if native.model != o.ActiveModel() {
		t.Fatalf("native runner model = %q, ActiveModel = %q", native.model, o.ActiveModel())
	}

	if err := o.SetTaskModel(context.Background(), settings.RoleOrchestrator, "legacy", "codex", "auto"); err != nil {
		t.Fatalf("SetTaskModel legacy auto: %v", err)
	}
	if got := o.ActiveModel(); got != "" {
		t.Fatalf("legacy auto inherited native model %q", got)
	}
	runner, err = o.runnerForRole(settings.RoleOrchestrator)
	if err != nil {
		t.Fatalf("legacy runnerForRole: %v", err)
	}
	legacy, ok := runner.(*legacyRunner)
	if !ok {
		t.Fatalf("runner = %T, want *legacyRunner", runner)
	}
	if legacy.model != "" {
		t.Fatalf("legacy auto runner model = %q, want vendor default", legacy.model)
	}
}
