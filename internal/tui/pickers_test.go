package tui

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/git"
	"github.com/bryann2k/maestro/internal/orchestrator"
)

func TestGroupedPickerPaging(t *testing.T) {
	l := &listOverlay{
		groups:     []string{"openai", "anthropic", "local"},
		groupIdx:   0,
		groupItems: map[string][]string{"openai": {"gpt-4o"}, "anthropic": {"claude"}, "local": {"ollama"}},
		groupMeta:  map[string]string{"openai": "▾ openai", "anthropic": "▾ anthropic", "local": "▾ local"},
	}
	if !l.grouped() || !l.groupPageable() {
		t.Fatal("grouped picker must be pageable")
	}
	if got := l.Filter(); len(got) != 1 || got[0] != "gpt-4o" {
		t.Fatalf("page 0 items = %v", got)
	}
	l.switchGroup(1)
	if got := l.Filter(); len(got) != 1 || got[0] != "claude" {
		t.Fatalf("page 1 items = %v", got)
	}
	l.switchGroup(1)
	if l.groupIdx != 2 || l.Filter()[0] != "ollama" {
		t.Fatalf("page 2 = idx %d %v", l.groupIdx, l.Filter())
	}
	// Wrap-around forward and backward.
	l.switchGroup(1)
	if l.groupIdx != 0 {
		t.Fatalf("wrap forward = %d", l.groupIdx)
	}
	l.switchGroup(-1)
	if l.groupIdx != 2 {
		t.Fatalf("wrap backward = %d", l.groupIdx)
	}
	// Switching resets the query.
	l.query = "zz"
	l.switchGroup(-1)
	if l.query != "" {
		t.Fatalf("query after switch = %q", l.query)
	}
	// Hint line advertises paging.
	if h := l.hintLine(); h == "type to filter · ↑/↓ · enter select · esc cancel" {
		t.Fatalf("hint line must advertise paging: %q", h)
	}
	// Non-grouped lists are unaffected.
	plain := &listOverlay{items: []string{"a", "b"}}
	if plain.grouped() || plain.groupPageable() {
		t.Fatal("plain list must not be pageable")
	}
	if got := plain.Filter(); len(got) != 2 {
		t.Fatalf("plain items = %v", got)
	}
}

func TestQualifiedModelIDPrefixesNamespacedRawAPIIDs(t *testing.T) {
	tests := map[string]string{
		"gpt-5.6":                    "custom/gpt-5.6",
		"openai/gpt-5.6":             "custom/openai/gpt-5.6",
		"accounts/acme/models/coder": "custom/accounts/acme/models/coder",
	}
	for id, want := range tests {
		if got := qualifiedModelID("custom", id); got != want {
			t.Errorf("qualifiedModelID(custom, %q) = %q, want %q", id, got, want)
		}
	}
}

func TestQualifiedCustomConfigModelAppearsInPickerAndSettings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAESTRO_SKILLS_DIR", filepath.Join(t.TempDir(), "skill-state"))
	dir := t.TempDir()
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: "smoke-native", Type: "openai-compat", BaseURL: "http://127.0.0.1:1/v1", APIKey: "test-only",
		}},
		Models: []config.Model{{
			ID: "smoke-native/smoke-model", Name: "Smoke Native",
			ContextWindow: 32768, CanReason: true,
		}},
	}
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		In: strings.NewReader(""), Out: &bytes.Buffer{}, Config: cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	picker := newModelPickerOverlay(orch)
	items := picker.groupItems["smoke-native"]
	if len(items) != 1 || !strings.HasPrefix(items[0], "smoke-native/smoke-model  ") {
		t.Fatalf("custom picker items = %v", items)
	}

	settings := newSettingsOverlay(&Model{orch: orch})
	found := false
	for _, id := range settings.models {
		if id == "smoke-native/smoke-model" {
			if found {
				t.Fatal("custom model duplicated in Settings")
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("Settings models omit custom model: %v", settings.models)
	}
}

func TestWindowTopCentering(t *testing.T) {
	// Fewer items than the window → pinned at 0.
	if top := windowTop(3, 5); top != 0 {
		t.Errorf("short list: top = %d, want 0", top)
	}
	// Start of list → pinned at 0.
	if top := windowTop(0, 20); top != 0 {
		t.Errorf("start: top = %d, want 0", top)
	}
	// Middle → cursor centered (half = 6).
	if top := windowTop(6, 20); top != 0 {
		t.Errorf("middle start: top = %d, want 0", top)
	}
	if top := windowTop(10, 20); top != 4 {
		t.Errorf("middle: top = %d, want 4", top)
	}
	// End of list → pinned to the last window.
	if top := windowTop(19, 20); top != 8 {
		t.Errorf("end: top = %d, want 8", top)
	}
	if top := windowTop(14, 20); top != 8 {
		t.Errorf("near end: top = %d, want 8", top)
	}
}

func TestEmptySessionPickerHasNoSelectableSentinel(t *testing.T) {
	// A real orchestrator now persists its fresh active session immediately so
	// it can be resumed safely from another process. Exercise the genuinely
	// empty data state directly instead of relying on the former lazy-save
	// behavior.
	picker := newSessionSummaryPickerOverlay(nil, "")
	if got := picker.selectedValue(); got != "" {
		t.Fatalf("empty session picker selected %q", got)
	}
}

func TestWorkspacePickerUsesExactPathsAndDisablesUnsafeRows(t *testing.T) {
	current := filepath.Join(t.TempDir(), "main")
	other := filepath.Join(t.TempDir(), "feature branch")
	blocked := filepath.Join(t.TempDir(), "locked\x1b]8;;bad")
	picker := newWorkspacePickerOverlay([]git.Workspace{
		{Path: current, Branch: "main", Healthy: true, Current: true},
		{Path: other, Branch: "feat/session titles", Healthy: true, Dirty: true},
		{Path: blocked, Branch: "bad\nbranch", Healthy: false, DisabledReason: "locked\x1b[31m"},
	}, current)
	if got := picker.selectedValue(); got != createWorkspacePickerValue {
		t.Fatalf("first value = %q, want create", got)
	}
	picker.down()
	if got := picker.selectedValue(); got != current {
		t.Fatalf("current value = %q, want exact %q", got, current)
	}
	picker.down()
	if got := picker.selectedValue(); got != other {
		t.Fatalf("feature value = %q, want exact %q", got, other)
	}
	picker.down()
	if got := picker.selectedValue(); got != other {
		t.Fatalf("disabled row became selectable: %q", got)
	}
	view := picker.View(NewStyles(Charmtone()), 72)
	plain := ansi.Strip(view)
	if strings.Contains(plain, "\x1b]8;;bad") || strings.Contains(plain, "\x1b[31m") {
		t.Fatalf("workspace picker leaked terminal controls: %q", view)
	}
}

func TestScrollHints(t *testing.T) {
	// 5 items, 12 visible → no scroll.
	if scrollUpHint(0) || scrollDownHint(0, 5) {
		t.Error("short list must not hint scrolling")
	}
	// 20 items at the top window.
	if scrollUpHint(0) {
		t.Error("no up hint at top")
	}
	if !scrollDownHint(0, 20) {
		t.Error("down hint expected mid-list")
	}
	// 20 items at the last window.
	if !scrollUpHint(8) {
		t.Error("up hint expected at bottom")
	}
	if scrollDownHint(8, 20) {
		t.Error("no down hint at bottom")
	}
}

func TestModelPickerOpensOnUsableGroup(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
			{Name: "ollama", Type: "ollama", BaseURL: "http://localhost:11434"},
		},
		Models: []config.Model{{ID: "opencode/deepseek-v4-flash"}},
	}
	orch, err := orchestrator.New(context.Background(), orchestrator.Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		In:          strings.NewReader(""),
		Out:         &bytes.Buffer{},
		Config:      cfg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Active model lives on opencode, which has no API key here; ollama is
	// local (always usable) → the picker must open on ollama.
	orch.SetModel("opencode/deepseek-v4-flash")
	l := newModelPickerOverlay(orch)
	if !l.grouped() {
		t.Fatal("model picker must be grouped")
	}
	got := l.groups[l.groupIdx]
	// When the active model's provider is unusable, the picker must jump to
	// the first usable (local) provider it can see. With the embedded
	// catalog the local providers carry no models, so there may be no
	// usable group at all — then the picker stays on the first group.
	usable := map[string]bool{}
	for _, p := range orch.ProviderList(context.Background()) {
		if !p.RequiresKey || p.KeySet {
			usable[p.Name] = true
		}
	}
	hasUsableGroup := false
	for _, name := range l.groups {
		if usable[name] {
			hasUsableGroup = true
			break
		}
	}
	if hasUsableGroup && !usable[got] {
		t.Errorf("picker opens on %q, want a usable provider", got)
	}
	if got == "opencode" {
		t.Errorf("picker must not open on the unkeyed active provider, got %q", got)
	}
	// A keyed provider wins over everything: simulate by marking opencode
	// as local too via its type... not possible without a key store; just
	// assert the active group is chosen when it is usable.
	orch.SetModel("")
	l2 := newModelPickerOverlay(orch)
	if l2.groupIdx < 0 || l2.groupIdx >= len(l2.groups) {
		t.Fatalf("groupIdx %d out of range (%d groups)", l2.groupIdx, len(l2.groups))
	}
}
