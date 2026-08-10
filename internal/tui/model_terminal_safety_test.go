package tui

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/orchestrator"
	"github.com/bryann2k/maestro/internal/settings"
)

func TestModelMetadataIsSanitizedOnlyAtTUIRenderBoundaries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	const (
		provider = "smoke-native"
		apiID    = "accounts/acme/models/coder\x1b[2J\u202e"
		name     = "Coder\x1b]52;c;name\x07\u2066"
	)
	handle := provider + "/" + apiID
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: provider, Type: "openai-compat", BaseURL: "https://smoke.invalid/v1", APIKey: "test-only",
		}},
		Models: []config.Model{{ID: handle, Name: name, ContextWindow: 32768}},
	}
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.SetModel(handle)
	if got := orch.ActiveModel(); got != handle {
		t.Fatalf("canonical active model changed: %q, want %q", got, handle)
	}
	if err := orch.SetTaskModel(context.Background(), settings.RoleOrchestrator, "native", "", handle); err != nil {
		t.Fatalf("SetTaskModel: %v", err)
	}

	styles := NewStyles(Charmtone())
	picker := newModelPickerOverlay(orch)
	for i, group := range picker.groups {
		if group == provider {
			picker.groupIdx = i
			break
		}
	}
	if got := picker.selectedValue(); got != handle {
		t.Fatalf("picker canonical value = %q, want %q", got, handle)
	}
	assertNoModelTerminalInjection(t, "picker", picker.View(styles, 120))

	models := newTaskModelOverlay(orch)
	for i, source := range models.sources {
		if source.id == provider {
			models.source = i
			break
		}
	}
	assertNoModelTerminalInjection(t, "model workspace", models.viewSized(styles, 120, 30))
	if source := models.currentSource(); source == nil || len(source.models) != 1 || source.models[0].id != handle {
		t.Fatalf("workspace canonical route lost: %+v", source)
	}

	providers := newProvidersOverlay(orch, provider)
	assertNoModelTerminalInjection(t, "provider workspace", providers.viewSized(styles, 120, 30))
	if card := providers.current(); card == nil || card.id != provider {
		t.Fatalf("provider workspace canonical id lost: %+v", card)
	}

	settingsView := newSettingsOverlay(&Model{orch: orch})
	settingsView.section = settingsProviders
	for i, item := range settingsView.providers {
		if item.ID == provider {
			settingsView.selected = i
			break
		}
	}
	assertNoModelTerminalInjection(t, "settings providers", settingsView.viewSized(styles, 120, 30))
	settingsView.section = settingsAgents
	settingsView.selected = 2 // orchestrator model
	assertNoModelTerminalInjection(t, "settings model route", settingsView.viewSized(styles, 120, 30))
	foundCanonical := false
	for _, model := range settingsView.models {
		foundCanonical = foundCanonical || model == handle
	}
	if !foundCanonical {
		t.Fatalf("Settings lost canonical model handle: %q", handle)
	}

	m := &Model{orch: orch, styles: styles}
	assertNoModelTerminalInjection(t, "runtime chrome", m.renderRuntimeChrome(160))
	auth := newTaskAuthOverlay(provider, handle, settings.RoleOrchestrator)
	assertNoModelTerminalInjection(t, "auth dialog", auth.View(styles, 100))
	if auth.provider != provider || auth.model != handle {
		t.Fatal("auth dialog mutated canonical routing values")
	}
	if got := orch.ActiveModel(); got != handle {
		t.Fatalf("rendering mutated active model: %q", got)
	}
}

func TestModelPickerSanitizedLabelCollisionPreservesCanonicalValues(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	const provider = "smoke"
	first := provider + "/model\x1b"
	second := provider + "/model␛"
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: provider, Type: "openai-compat", BaseURL: "https://smoke.invalid/v1", APIKey: "test-only",
		}},
		Models: []config.Model{{ID: first}, {ID: second}},
	}
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir: t.TempDir(), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	picker := newModelPickerOverlay(orch)
	items := picker.groupItems[provider]
	if len(items) != 2 || items[0] == items[1] {
		t.Fatalf("sanitized collision was not disambiguated: %q", items)
	}
	got := map[string]bool{}
	for i := range items {
		picker.selected = i
		got[picker.selectedValue()] = true
	}
	if !got[first] || !got[second] || len(got) != 2 {
		t.Fatalf("canonical values after display collision = %v", got)
	}
}

func assertNoModelTerminalInjection(t *testing.T, surface, rendered string) {
	t.Helper()
	for _, forbidden := range []string{"\x1b]52", "\x1b[2J", "\u202e", "\u2066"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("%s rendered unsafe terminal control %q: %q", surface, forbidden, rendered)
		}
	}
}
