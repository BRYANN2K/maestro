package orchestrator

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/settings"
)

func assertExternalHarnessRejected(t *testing.T) {
	t.Helper()
	o := newTestOrch(t, newTestRepo(t), nil)
	choices := o.EngineChoices("dev")
	if len(choices) != 1 || choices[0].Engine != "native" {
		t.Fatalf("choices=%+v", choices)
	}
	for _, name := range []string{"codex", "claude", "cursor", "opencode"} {
		if err := o.SetTaskModel(t.Context(), "dev", "legacy", name, "auto"); err == nil {
			t.Fatalf("accepted %s", name)
		}
		if _, err := o.buildRunner(BuildOptions{Engine: "legacy", Agent: name}); err == nil {
			t.Fatalf("built %s", name)
		}
	}
	if _, err := (&legacyRunner{o: o}).Run(t.Context(), agentcore.RoleDev, "do not execute"); err == nil {
		t.Fatal("external runner executed")
	}
}
func TestOnlyMaestroCanExecuteAgents(t *testing.T) { assertExternalHarnessRejected(t) }

func TestNativeRoutePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	o, err := New(context.Background(), Options{ProjectDir: t.TempDir(), SessionsDir: t.TempDir(), Settings: settings.Defaults(), SettingsPath: path, Config: config.New()})
	if err != nil {
		t.Fatal(err)
	}
	defer o.Close()
	if err = o.SetTaskModel(t.Context(), "dev", "native", "", "custom/coder"); err != nil {
		t.Fatal(err)
	}
	loaded, err := settings.Load(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if r := loaded.RoleDefaults["dev"]; r.Engine != "native" || r.Agent != "" || r.Model != "custom/coder" {
		t.Fatalf("route=%+v", r)
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
