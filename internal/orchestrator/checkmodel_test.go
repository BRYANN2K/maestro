package orchestrator

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/settings"
)

// newModelOrch builds an orchestrator over a config with one static model.
func newModelOrch(t *testing.T) *Orchestrator {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
		},
		Models: []config.Model{{ID: "opencode/deepseek-v4-flash"}},
	}
	var out bytes.Buffer
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader("n\n"),
		Out:         &out,
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return orch
}

func TestModelCheckErrorKnown(t *testing.T) {
	orch := newModelOrch(t)
	orch.SetModel("opencode/deepseek-v4-flash")
	if err := orch.ModelCheckError(); err != nil {
		t.Errorf("known model rejected: %v", err)
	}
}

func TestModelCheckErrorUnknownWithSuggestion(t *testing.T) {
	orch := newModelOrch(t)
	orch.SetModel("opencode/deepseek-v4-flash-free")
	err := orch.ModelCheckError()
	if err == nil {
		t.Fatal("unknown model accepted")
	}
	if !strings.Contains(err.Error(), "not served by provider") {
		t.Errorf("error should mention the provider: %v", err)
	}
	if !strings.Contains(err.Error(), "closest match") || !strings.Contains(err.Error(), "deepseek-v4-flash") {
		t.Errorf("error should suggest the closest catalog model: %v", err)
	}
}

func TestModelCheckErrorNoModel(t *testing.T) {
	orch := newModelOrch(t)
	if err := orch.ModelCheckError(); err != nil {
		t.Errorf("empty model should pass preflight: %v", err)
	}
}

func TestCanonicalModelSendsBareID(t *testing.T) {
	orch := newModelOrch(t)
	// Bare config entries (the old maestro's settings.Provider.Model) are
	// sent as-is.
	if got := orch.canonicalModel("deepseek-v4-flash"); got != "deepseek-v4-flash" {
		t.Errorf("canonicalModel = %q, want deepseek-v4-flash", got)
	}
	// Genuinely-qualified ids pass through unchanged.
	if got := orch.canonicalModel("accounts/fireworks/models/deepseek-v4-flash"); got != "accounts/fireworks/models/deepseek-v4-flash" {
		t.Errorf("qualified id must pass through, got %q", got)
	}
	// Unknown ids stay untouched (the preflight reports them earlier).
	if got := orch.canonicalModel("opencode/nope"); got != "opencode/nope" {
		t.Errorf("unknown id must pass through, got %q", got)
	}
}

func TestModelCheckErrorListsAvailableProviders(t *testing.T) {
	orch := newModelOrch(t)
	orch.SetModel("nope/missing")
	err := orch.ModelCheckError()
	if err == nil {
		t.Fatal("unknown model accepted")
	}
	if !strings.Contains(err.Error(), "available") {
		t.Errorf("error should list available providers: %v", err)
	}
}

func TestDefaultModelFromSettingsSlots(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Settings: settings.Settings{
			ModelSlots: map[string]string{"large": "opencode/deepseek-v4-flash"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := orch.defaultModel(); got != "opencode/deepseek-v4-flash" {
		t.Errorf("defaultModel = %q, want the settings slot", got)
	}
	// Config roles win over settings when present.
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "openai", Type: "openai"}},
	}
	orch2, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Config:      cfg,
		Settings: settings.Settings{
			ModelSlots: map[string]string{"large": "settings-fallback"},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := orch2.defaultModel(); got != "settings-fallback" {
		t.Errorf("defaultModel with empty roles = %q, want settings fallback", got)
	}
}

func TestActiveModelUsesPersistedOrchestratorRouteBeforeSlot(t *testing.T) {
	dir := t.TempDir()
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Settings: settings.Settings{
			ModelSlots: map[string]string{"large": "slot-fallback"},
			RoleDefaults: map[string]settings.RoleDefaults{
				settings.RoleOrchestrator: {Engine: "legacy", Agent: "codex", Model: "gpt-5.6-luna"},
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := orch.ActiveModel(); got != "gpt-5.6-luna" {
		t.Fatalf("ActiveModel = %q, want persisted orchestrator route", got)
	}
}
