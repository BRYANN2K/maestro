package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/vault"
)

func TestProviderListSources(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	dir := newTestRepo(t)
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "openai", Type: "openai-compat", BaseURL: "https://api.openai.com/v1"}},
	}
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config:      cfg,
		Keys:        mapKeyStore{},
		In:          strings.NewReader(""),
		Out:         &out,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	list := orch.ProviderList(context.Background())
	sources := map[string]string{}
	for _, p := range list {
		sources[p.Name] = p.Source
	}
	if sources["openai"] != "maestrorc" {
		t.Errorf("openai source = %q", sources["openai"])
	}
	if sources["ollama"] != "local" {
		t.Errorf("ollama source = %q", sources["ollama"])
	}
	// ModelList includes catalog models.
	models := orch.ModelList(context.Background())
	found := false
	for _, m := range models {
		if m.Provider == "openai" && m.ID == "gpt-5" && m.PriceIn == 1.25 {
			found = true
		}
	}
	if !found {
		t.Error("catalog gpt-5 pricing missing from model list")
	}
}

type mapKeyStore map[string]string

func (m mapKeyStore) Key(name string) (string, bool) {
	v, ok := m[name]
	return v, ok
}

func TestProviderAddRemove(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	ctx := context.Background()

	p := config.Provider{Name: "mystery", Type: "openai-compat", BaseURL: "https://mystery.example/v1"}
	if err := orch.ProviderAdd(ctx, p, false); err != nil {
		t.Fatalf("ProviderAdd: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "maestrorc"))
	if err != nil || !strings.Contains(string(data), "provider add mystery") {
		t.Fatalf("maestrorc = %q, %v", data, err)
	}
	if err := orch.ProviderRemove(ctx, "mystery", false); err != nil {
		t.Fatalf("ProviderRemove: %v", err)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "maestrorc"))
	if strings.Contains(string(data), "provider add mystery") {
		t.Error("provider not removed")
	}
}

func TestProviderAddStoresAPIKeyOnlyInVault(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	v, err := vault.Open(context.Background(), filepath.Join(t.TempDir(), "vault.json"))
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	orch.vault = v
	secret := "sk-production-secret"
	if err := orch.ProviderAdd(context.Background(), config.Provider{
		Name: "private", Type: "openai-compat", BaseURL: "https://example.test/v1", APIKey: secret,
	}, false); err != nil {
		t.Fatalf("ProviderAdd: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "maestrorc"))
	if err != nil {
		t.Fatalf("read maestrorc: %v", err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "--api-key") {
		t.Fatalf("maestrorc leaked API key: %q", data)
	}
	if got, ok := v.Get("key:private"); !ok || got != secret {
		t.Fatalf("vault key = %q, %v", got, ok)
	}
	info, err := os.Stat(filepath.Join(dir, "maestrorc"))
	if err != nil {
		t.Fatalf("stat maestrorc: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("maestrorc mode = %o, want 600", got)
	}
}

func TestProviderAddWithAPIKeyRequiresVault(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	err := orch.ProviderAdd(context.Background(), config.Provider{
		Name: "private", Type: "openai-compat", BaseURL: "https://example.test/v1", APIKey: "sk-secret",
	}, false)
	if err == nil || !strings.Contains(err.Error(), "vault unavailable") {
		t.Fatalf("ProviderAdd error = %v, want vault unavailable", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "maestrorc")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("maestrorc should not be created, stat error = %v", statErr)
	}
}

func TestProviderAddValidationPrecedesVaultAndConfigMutation(t *testing.T) {
	invalid := []config.Provider{
		{Name: "has space", Type: "openai-compat", BaseURL: "https://invalid.test/v1", APIKey: "secret"},
		{Name: "line\noption injected true", Type: "openai-compat", BaseURL: "https://invalid.test/v1", APIKey: "secret"},
		{Name: "has/slash", Type: "openai-compat", BaseURL: "https://invalid.test/v1", APIKey: "secret"},
		{Name: strings.Repeat("a", config.MaxProviderIDLength+1), Type: "openai-compat", BaseURL: "https://invalid.test/v1", APIKey: "secret"},
		{Name: "missing-base", Type: "openai-compat", APIKey: "secret"},
		{Name: "unsupported", Type: "unknown", BaseURL: "https://invalid.test/v1", APIKey: "secret"},
	}
	for _, provider := range invalid {
		t.Run(provider.Name, func(t *testing.T) {
			dir := newTestRepo(t)
			orch := newTestOrch(t, dir, &fakeRunner{})
			v, err := vault.Open(context.Background(), filepath.Join(t.TempDir(), "vault.json"))
			if err != nil {
				t.Fatalf("open vault: %v", err)
			}
			orch.vault = v
			if err := orch.ProviderAdd(context.Background(), provider, false); err == nil {
				t.Fatalf("ProviderAdd(%+v) succeeded", provider)
			}
			if v.Len() != 0 {
				t.Fatalf("invalid provider mutated vault: keys=%v", v.Keys())
			}
			if _, err := os.Stat(filepath.Join(dir, "maestrorc")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid provider wrote config: %v", err)
			}
		})
	}
}

func TestDispatchProviderList(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{}
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config:      cfg,
		In:          strings.NewReader(""),
		Out:         &out,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := orch.Dispatch(context.Background(), Command{Cmd: "provider", Args: []string{"list"}}); err != nil {
		t.Fatalf("provider list: %v", err)
	}
	if !strings.Contains(out.String(), "ollama") {
		t.Errorf("provider list output = %q", out.String())
	}
}

func TestDispatchProviderAndModelRejectExtraPositionalsWithoutWriting(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	commands := []Command{
		{Cmd: "provider", Args: []string{"list", "accidental"}},
		{Cmd: "provider", Args: []string{"list"}, Flags: map[string]string{"type": "openai-compat"}},
		{Cmd: "provider", Args: []string{"add", "custom", "accidental"}, Flags: map[string]string{"type": "openai-compat", "base-url": "https://custom.test/v1"}},
		{Cmd: "provider", Args: []string{"remove", "custom", "accidental"}},
		{Cmd: "provider", Args: []string{"remove", "custom"}, Flags: map[string]string{"base-url": "https://ignored.test/v1"}},
		{Cmd: "model", Args: []string{"list", "accidental"}},
	}
	for _, command := range commands {
		if err := orch.Dispatch(context.Background(), command); err == nil {
			t.Fatalf("Dispatch(%s %v) succeeded", command.Cmd, command.Args)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "maestrorc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("malformed dispatch wrote config: %v", err)
	}
}

func TestAuthLoginStatusLogout(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	dir := newTestRepo(t)
	vaultPath := filepath.Join(t.TempDir(), "vault.json")
	v, err := vault.Open(context.Background(), vaultPath)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config:      &config.Config{},
		In:          strings.NewReader(""),
		Out:         &out,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.vault = v
	ctx := context.Background()

	// login via dispatch with a piped key
	orch.in = strings.NewReader("sk-secret-key\n")
	orch.out = &out
	if err := orch.Dispatch(ctx, Command{Cmd: "auth", Args: []string{"login", "openai"}}); err != nil {
		t.Fatalf("auth login: %v", err)
	}
	got, ok := v.Get("key:openai")
	if !ok || got != "sk-secret-key" {
		t.Fatalf("vault key = %q, %v", got, ok)
	}
	// status shows the key
	out.Reset()
	if err := orch.Dispatch(ctx, Command{Cmd: "auth", Args: []string{"status"}}); err != nil {
		t.Fatalf("auth status: %v", err)
	}
	if !strings.Contains(out.String(), "api key") {
		t.Errorf("auth status = %q", out.String())
	}
	// logout removes it
	if err := orch.Dispatch(ctx, Command{Cmd: "auth", Args: []string{"logout", "openai"}}); err != nil {
		t.Fatalf("auth logout: %v", err)
	}
	if _, ok := v.Get("key:openai"); ok {
		t.Error("key should be removed")
	}
}

func TestAuthOAuthRefusesBeforeStartingFlow(t *testing.T) {
	dir := newTestRepo(t)
	v, err := vault.Open(context.Background(), filepath.Join(t.TempDir(), "vault.json"))
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config: &config.Config{}, In: strings.NewReader(""), Out: &out,
		Runner: &fakeRunner{}, Vault: v,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	err = orch.AuthOAuth(context.Background(), "github-copilot")
	if err == nil || !strings.Contains(err.Error(), "not supported by the native provider runtime") {
		t.Fatalf("AuthOAuth error = %v, want explicit runtime capability error", err)
	}
	if out.Len() != 0 {
		t.Fatalf("AuthOAuth started or announced a flow: %q", out.String())
	}
	if v.Len() != 0 {
		t.Fatalf("AuthOAuth stored credentials despite rejection: keys=%v", v.Keys())
	}
}

func TestAuthStatusMarksLegacyOAuthTokenUnsupported(t *testing.T) {
	dir := newTestRepo(t)
	v, err := vault.Open(context.Background(), filepath.Join(t.TempDir(), "vault.json"))
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	v.Set("oauth:codex:access", "legacy-token")
	if err := v.Save(context.Background()); err != nil {
		t.Fatalf("save vault: %v", err)
	}
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config: &config.Config{}, In: strings.NewReader(""), Out: &out,
		Runner: &fakeRunner{}, Vault: v,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	orch.AuthStatus(context.Background())
	if got := out.String(); !strings.Contains(got, "codex") || !strings.Contains(got, "oauth stored (runtime unsupported)") {
		t.Fatalf("AuthStatus = %q", got)
	}
}

func TestDispatchModelList(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	dir := newTestRepo(t)
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir:  dir,
		SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config:      &config.Config{},
		In:          strings.NewReader(""),
		Out:         &out,
		Runner:      &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := orch.Dispatch(context.Background(), Command{Cmd: "model", Args: []string{"list"}, Flags: map[string]string{"json": "true"}}); err != nil {
		t.Fatalf("model list: %v", err)
	}
	if !strings.Contains(out.String(), "gpt-5") {
		t.Errorf("model list = %q", out.String())
	}
}

func TestModelListIncludesQualifiedCustomConfigModel(t *testing.T) {
	dir := newTestRepo(t)
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: "smoke-native", Type: "openai-compat", BaseURL: "http://127.0.0.1:1/v1",
		}},
		Models: []config.Model{{
			ID: "smoke-native/smoke-model", Name: "Smoke Native",
			ContextWindow: 32768, CanReason: true, PriceInput: 0.25, PriceOutput: 0.75,
		}},
	}
	orch, err := New(context.Background(), Options{
		ProjectDir: dir, SessionsDir: filepath.Join(t.TempDir(), "s"),
		Config: cfg, In: strings.NewReader(""), Out: &strings.Builder{},
		Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var matches []ModelInfo
	for _, model := range orch.ModelList(context.Background()) {
		if model.Provider == "smoke-native" && model.ID == "smoke-model" {
			matches = append(matches, model)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("custom model matches = %+v, want exactly one", matches)
	}
	if got := matches[0]; got.Name != "Smoke Native" || got.Context != 32768 || !got.Reasoning || got.PriceIn != 0.25 || got.PriceOut != 0.75 {
		t.Fatalf("custom ModelList metadata = %+v", got)
	}
}

func TestProviderAndModelListingsNeutralizeTerminalControls(t *testing.T) {
	const (
		provider = "smoke-native"
		apiID    = "accounts/acme/models/coder\x1b[2J\u202e"
		name     = "Coder\x1b]52;c;name\x07\u2066"
	)
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: provider, Type: "openai-compat", BaseURL: "https://smoke.invalid/v1", APIKey: "test-only",
		}},
		Models: []config.Model{{ID: provider + "/" + apiID, Name: name}},
	}
	var out strings.Builder
	orch, err := New(context.Background(), Options{
		ProjectDir: newTestRepo(t), SessionsDir: filepath.Join(t.TempDir(), "sessions"),
		Config: cfg, In: strings.NewReader(""), Out: &out, Runner: &fakeRunner{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := orch.Dispatch(context.Background(), Command{Cmd: "provider", Args: []string{"list"}}); err != nil {
		t.Fatalf("provider list: %v", err)
	}
	assertNoHeadlessTerminalInjection(t, "provider list", out.String())

	out.Reset()
	if err := orch.Dispatch(context.Background(), Command{Cmd: "model", Args: []string{"list"}, Flags: map[string]string{"provider": provider}}); err != nil {
		t.Fatalf("model list: %v", err)
	}
	assertNoHeadlessTerminalInjection(t, "model list", out.String())

	out.Reset()
	if err := orch.Dispatch(context.Background(), Command{
		Cmd: "model", Args: []string{"list"}, Flags: map[string]string{"provider": provider, "json": "true"},
	}); err != nil {
		t.Fatalf("model list json: %v", err)
	}
	jsonOutput := out.String()
	assertNoHeadlessTerminalInjection(t, "model list json", jsonOutput)
	if !strings.Contains(jsonOutput, `\u001b`) || !strings.Contains(jsonOutput, `\u202e`) || !strings.Contains(jsonOutput, `\u2066`) {
		t.Fatalf("JSON controls were not escaped: %q", jsonOutput)
	}
	var decoded []ModelInfo
	if err := json.Unmarshal([]byte(jsonOutput), &decoded); err != nil {
		t.Fatalf("decode model JSON: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Provider != provider || decoded[0].ID != apiID || decoded[0].Name != name {
		t.Fatalf("JSON did not preserve canonical data: %+v", decoded)
	}
}

func assertNoHeadlessTerminalInjection(t *testing.T, surface, output string) {
	t.Helper()
	for _, forbidden := range []string{"\x1b", "\x07", "\u202e", "\u2066"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("%s emitted raw terminal control %q: %q", surface, forbidden, output)
		}
	}
}

func TestProviderAddWritesProjectConfig(t *testing.T) {
	dir := newTestRepo(t)
	orch := newTestOrch(t, dir, &fakeRunner{})
	var out strings.Builder
	orch.out = &out
	if err := orch.Dispatch(context.Background(), Command{
		Cmd: "provider", Args: []string{"add", "custom"},
		Flags: map[string]string{"type": "openai-compat", "base-url": "http://x/v1"},
	}); err != nil {
		t.Fatalf("provider add: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "maestrorc"))
	if !strings.Contains(string(data), "--base-url") {
		t.Errorf("maestrorc = %q", data)
	}
}
