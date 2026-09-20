package agentcore

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
)

func TestResolveRole(t *testing.T) {
	slots := map[string]Slot{
		"large": {Model: "openai/gpt-4o", Sampling: Sampling{ReasoningEffort: "low"}},
		"small": {Model: "openai/gpt-4o-mini"},
	}
	roles := map[string]Slot{
		"commit": {Model: "deepseek/deepseek-chat"},
	}
	tests := []struct {
		role   RoleName
		model  string
		think  bool
		effort string
		ok     bool
	}{
		{RoleDefault, "openai/gpt-4o", false, "low", true},
		{RoleSlow, "openai/gpt-4o", true, "high", true},
		{RolePlan, "openai/gpt-4o", false, "medium", true},
		{RoleSmol, "openai/gpt-4o-mini", false, "", true},
		{RoleCommit, "deepseek/deepseek-chat", false, "", true}, // explicit role wins
	}
	for _, tt := range tests {
		model, sm, ok := ResolveRole(tt.role, slots, roles)
		if ok != tt.ok || model != tt.model || sm.Think != tt.think || sm.ReasoningEffort != tt.effort {
			t.Errorf("ResolveRole(%s) = %q, %+v, %v", tt.role, model, sm, ok)
		}
	}
}

func TestResolveRoleEmpty(t *testing.T) {
	if _, _, ok := ResolveRole(RoleDefault, nil, nil); ok {
		t.Error("ResolveRole with no slots should fail")
	}
}

type mapKeyStore map[string]string

func (m mapKeyStore) Key(name string) (string, bool) {
	v, ok := m[name]
	return v, ok
}

type registryClosingProvider struct {
	fakeProvider
	closed int
}

func (p *registryClosingProvider) Close() error {
	p.closed++
	return nil
}

func TestRegistryCloseReleasesProviderResources(t *testing.T) {
	provider := &registryClosingProvider{}
	r := &Registry{}
	r.state.Store(&registryState{
		providers: map[string]Provider{"test": provider},
		models:    map[string]Model{},
		catalog:   map[string]CatalogProvider{},
	})
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if provider.closed != 1 {
		t.Fatalf("provider close calls = %d, want 1", provider.closed)
	}
}

func TestRegistryReplaceAndCloseReleasesOnlyRetiredProviders(t *testing.T) {
	retired := &registryClosingProvider{}
	retained := &registryClosingProvider{}
	target := &Registry{}
	target.state.Store(&registryState{
		providers: map[string]Provider{"retired": retired, "shared-a": retained, "shared-b": retained},
		models:    map[string]Model{}, catalog: map[string]CatalogProvider{},
	})
	next := &Registry{}
	next.state.Store(&registryState{
		providers: map[string]Provider{"shared": retained},
		models:    map[string]Model{}, catalog: map[string]CatalogProvider{},
	})

	if err := target.ReplaceAndClose(next); err != nil {
		t.Fatalf("ReplaceAndClose: %v", err)
	}
	if retired.closed != 1 || retained.closed != 0 {
		t.Fatalf("close counts after replacement: retired=%d retained=%d", retired.closed, retained.closed)
	}
	if err := target.Close(); err != nil {
		t.Fatalf("Close replacement: %v", err)
	}
	if retained.closed != 1 {
		t.Fatalf("shared provider close calls = %d, want 1", retained.closed)
	}
}

func TestRegistryBuild(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "openai", Type: "openai-compat", BaseURL: "https://api.openai.com/v1", APIKey: "sk-direct"},
			{Name: "deepseek", Type: "openai-compat", BaseURL: "https://api.deepseek.com/v1"},
			{Name: "anthropic", Type: "anthropic", BaseURL: "https://api.anthropic.com"},
			{Name: "ollama", Type: "ollama", BaseURL: "http://localhost:11434/v1", DiscoverModels: true},
			{Name: "disabled", Type: "anthropic", Disabled: true},
			{Name: "bad", Type: "bedrock"},
			{Name: "badoauth", Type: "anthropic-oauth"},
		},
		Models: []config.Model{
			{ID: "openai/gpt-4o", Name: "GPT-4o", PriceInput: 2.5, PriceOutput: 10},
		},
	}
	keys := mapKeyStore{"deepseek": "sk-ds"}
	r, err := NewRegistry(context.Background(), cfg, keys, nil)
	if err == nil {
		t.Fatal("registry should report unsupported providers")
	}
	if !strings.Contains(err.Error(), "bedrock") || !strings.Contains(err.Error(), "OAuth") {
		t.Errorf("registry errors = %v", err)
	}
	// maestrorc providers + always-on local catalog providers
	if len(r.Providers()) != 7 {
		t.Errorf("providers = %v", r.Providers())
	}
	p, ok := r.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek missing")
	}
	if p.Name() != "deepseek" {
		t.Errorf("deepseek name = %s", p.Name())
	}
	// API key resolved from the KeyStore.
	if _, ok := r.Provider("openai"); !ok {
		t.Fatal("openai missing")
	}
	if _, ok := r.Provider("disabled"); ok {
		t.Error("disabled provider should not be registered")
	}
	// Model lookup.
	m, ok := r.Model("openai/gpt-4o")
	if !ok || m.PriceInput != 2.5 {
		t.Errorf("model = %+v, %v", m, ok)
	}
	if _, ok := r.Model("does-not-exist"); ok {
		t.Error("unknown model should not resolve")
	}
}

func TestRegistryOllamaLocalNoKey(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "ollama", Type: "ollama", BaseURL: "http://localhost:11434/v1"}},
	}
	r, err := NewRegistry(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, _ := r.Provider("ollama")
	// Local providers must not require a key at build time; the stream
	// request itself would fail only if the server is unreachable.
	if p == nil {
		t.Fatal("ollama provider missing")
	}
}

func TestRegistryUnknownType(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "x", Type: "warp-drive"}},
	}
	_, err := NewRegistry(context.Background(), cfg, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "warp-drive") {
		t.Errorf("err = %v", err)
	}
}

func TestRegistryCatalogIsDeepCloned(t *testing.T) {
	input := registryTestCatalog("remote", "model-a")
	r, err := NewRegistry(context.Background(), &config.Config{}, mapKeyStore{"remote": "test-key"}, input)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	// Mutating the constructor input must not alter the published snapshot.
	provider := input["remote"]
	provider.Env[0] = "MUTATED_INPUT_KEY"
	model := provider.Models["model-a"]
	model.Modalities.Input[0] = "audio"
	provider.Models["model-a"] = model
	delete(input, "remote")

	got := r.Catalog()
	if got["remote"].Env[0] != "REMOTE_API_KEY" {
		t.Fatalf("catalog env mutated through constructor input: %+v", got["remote"].Env)
	}
	if modalities := got["remote"].Models["model-a"].Modalities.Input; len(modalities) != 1 || modalities[0] != "text" {
		t.Fatalf("catalog modalities mutated through constructor input: %v", modalities)
	}

	// Catalog itself must also return a deep copy, not a mutable backing map.
	returned := r.Catalog()
	returnedProvider := returned["remote"]
	returnedProvider.Env[0] = "MUTATED_RETURN_KEY"
	returnedModel := returnedProvider.Models["model-a"]
	returnedModel.Modalities.Input[0] = "video"
	returnedProvider.Models["model-a"] = returnedModel
	delete(returned, "remote")

	again := r.Catalog()
	if again["remote"].Env[0] != "REMOTE_API_KEY" || again["remote"].Models["model-a"].Modalities.Input[0] != "text" {
		t.Fatalf("Catalog exposed mutable state: %+v", again["remote"])
	}
}

func TestRegistryReplaceCatalogReplacesSnapshot(t *testing.T) {
	r, err := NewRegistry(context.Background(), &config.Config{}, nil, registryTestCatalog("old-remote", "old-model"))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	replacement := registryTestCatalog("new-remote", "new-model")
	if err := r.ReplaceCatalog(replacement); err != nil {
		t.Fatalf("ReplaceCatalog: %v", err)
	}

	got := r.Catalog()
	if _, exists := got["old-remote"]; exists {
		t.Fatal("provider absent from refreshed catalog was retained")
	}
	if _, exists := got["new-remote"]; !exists {
		t.Fatal("replacement provider missing")
	}
	for _, local := range []string{"ollama", "llamacpp", "lmstudio", "litellm"} {
		if _, exists := got[local]; !exists {
			t.Fatalf("local provider %q missing after replacement", local)
		}
	}

	provider := replacement["new-remote"]
	delete(provider.Models, "new-model")
	delete(replacement, "new-remote")
	if _, exists := r.Catalog()["new-remote"].Models["new-model"]; !exists {
		t.Fatal("replacement input mutated the published snapshot")
	}
}

func TestRegistryConcurrentReplacementAndReads(t *testing.T) {
	keys := mapKeyStore{"red": "test-key", "blue": "test-key"}
	red, err := NewRegistry(context.Background(), &config.Config{}, keys, registryTestCatalog("red", "red-model"))
	if err != nil {
		t.Fatalf("red registry: %v", err)
	}
	blue, err := NewRegistry(context.Background(), &config.Config{}, keys, registryTestCatalog("blue", "blue-model"))
	if err != nil {
		t.Fatalf("blue registry: %v", err)
	}
	target, err := NewRegistry(context.Background(), &config.Config{}, keys, registryTestCatalog("red", "red-model"))
	if err != nil {
		t.Fatalf("target registry: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			if i%2 == 0 {
				target.Replace(blue)
			} else {
				target.Replace(red)
			}
		}
	}()
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				catalog := target.Catalog()
				delete(catalog, "red") // only the isolated return value
				_ = target.Providers()
				_, _ = target.Provider("red")
				_, _ = target.Model("red/red-model")
				_, _ = target.ModelMetadata("blue/blue-model")
				_, _ = target.ProviderOf("red/red-model")
				_ = target.APIModelID("blue/blue-model")
				_ = target.ReasoningEfforts("red/red-model")
				_ = target.CheckModel("blue/blue-model")
			}
		}()
	}
	wg.Wait()

	target.Replace(red)
	if _, ok := target.Provider("red"); !ok {
		t.Fatal("final replacement was not published")
	}
	if _, ok := target.Provider("blue"); ok {
		t.Fatal("final replacement retained provider from previous snapshot")
	}
}

func registryTestCatalog(providerID, modelID string) map[string]CatalogProvider {
	model := CatalogModel{ID: modelID, Name: modelID, ToolCall: true}
	model.Limit.Context = 32_000
	model.Limit.Output = 4_096
	model.Modalities.Input = []string{"text"}
	model.Modalities.Output = []string{"text"}
	return map[string]CatalogProvider{
		providerID: {
			ID: providerID, Name: providerID, Env: []string{strings.ToUpper(providerID) + "_API_KEY"},
			API: "https://example.invalid/v1", Models: map[string]CatalogModel{modelID: model},
		},
	}
}
