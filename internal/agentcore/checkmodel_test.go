package agentcore

import (
	"context"
	"strings"
	"testing"

	"github.com/bryann2k/maestro/internal/config"
)

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
		},
		Models: []config.Model{{ID: "opencode/deepseek-v4-flash"}},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return reg
}

func TestCheckModelKnown(t *testing.T) {
	reg := testRegistry(t)
	if err := reg.CheckModel("opencode/deepseek-v4-flash"); err != nil {
		t.Errorf("known model rejected: %v", err)
	}
}

func TestCheckModelUnknownProviderModel(t *testing.T) {
	reg := testRegistry(t)
	err := reg.CheckModel("opencode/deepseek-v4-flash-free")
	if err == nil {
		t.Fatal("unknown model accepted")
	}
	if !strings.Contains(err.Error(), "not served by provider") {
		t.Errorf("error should name the provider: %v", err)
	}
}

func TestCheckModelUnknownProvider(t *testing.T) {
	reg := testRegistry(t)
	err := reg.CheckModel("nope/whatever")
	if err == nil {
		t.Fatal("unknown provider accepted")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Errorf("error should say not available: %v", err)
	}
}

func TestCheckModelDiscoverableExempt(t *testing.T) {
	cfg := &config.Config{}
	reg, err := NewRegistry(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// ollama is a local, live-discovery provider: unknown IDs pass preflight
	// (their model list may be empty until the first fetch).
	if err := reg.CheckModel("ollama/any-model"); err != nil {
		t.Errorf("discoverable provider should be exempt: %v", err)
	}
}

func TestCheckModelServesFromLiveCatalog(t *testing.T) {
	// A model released after the provider snapshot (between models.dev
	// refreshes) must stay selectable: the live catalog is consulted.
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
		},
		Models: []config.Model{{ID: "opencode/deepseek-v4-flash"}},
	}
	catalog := map[string]CatalogProvider{
		"opencode": {
			ID:  "opencode",
			API: "https://opencode.ai/zen/v1",
			Env: []string{"OPENCODE_API_KEY"},
			Models: map[string]CatalogModel{
				"deepseek-v4-flash":      {},
				"deepseek-v4-flash-free": {},
			},
		},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, catalog)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// Present in the live catalog but absent from the provider snapshot.
	if err := reg.CheckModel("opencode/deepseek-v4-flash-free"); err != nil {
		t.Errorf("fresh catalog model rejected: %v", err)
	}
	// The canonical ID resolves to the bare API id.
	if m, ok := reg.Model("opencode/deepseek-v4-flash-free"); !ok || m.ID != "deepseek-v4-flash-free" {
		t.Errorf("canonical id = %q, ok=%v", m.ID, ok)
	}
}

func TestAPIModelIDResolvesBare(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
		},
	}
	catalog := map[string]CatalogProvider{
		"opencode": {
			ID:  "opencode",
			API: "https://opencode.ai/zen/v1",
			Env: []string{"OPENCODE_API_KEY"},
			Models: map[string]CatalogModel{
				"deepseek-v4-flash-free": {},
			},
		},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, catalog)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// Picker handle → bare API id.
	if got := reg.APIModelID("opencode/deepseek-v4-flash-free"); got != "deepseek-v4-flash-free" {
		t.Errorf("APIModelID = %q, want deepseek-v4-flash-free", got)
	}
	// Genuinely-qualified ids pass through unchanged.
	if got := reg.APIModelID("accounts/fireworks/models/deepseek-v4-flash"); got != "accounts/fireworks/models/deepseek-v4-flash" {
		t.Errorf("qualified id must pass through, got %q", got)
	}
	// Bare ids pass through.
	if got := reg.APIModelID("deepseek-v4-flash-free"); got != "deepseek-v4-flash-free" {
		t.Errorf("bare id must pass through, got %q", got)
	}
}

func TestCheckModelBareConfigEntry(t *testing.T) {
	// Bare model-add entries resolve to the sole configured provider.
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "opencode", Type: "openai-compat", BaseURL: "https://opencode.ai/zen/v1"},
		},
		Models: []config.Model{{ID: "deepseek-v4-flash-free"}},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if p, ok := reg.ProviderOf("deepseek-v4-flash-free"); !ok || p != "opencode" {
		t.Errorf("bare model provider = %q, ok=%v", p, ok)
	}
	if err := reg.CheckModel("deepseek-v4-flash-free"); err != nil {
		t.Errorf("bare config model rejected: %v", err)
	}
}

func TestQualifiedConfigModelIsAttachedToCustomProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: "smoke-native", Type: "openai-compat", BaseURL: "http://127.0.0.1:1/v1",
		}},
		Models: []config.Model{{
			ID: "smoke-native/smoke-model", Name: "Smoke Native",
			ContextWindow: 32768, CanReason: true, PriceInput: 0.25,
		}},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, map[string]CatalogProvider{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	models, err := reg.Models(context.Background(), "smoke-native")
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 {
		t.Fatalf("custom models = %+v, want one configured model", models)
	}
	if got := models[0]; got.ID != "smoke-model" || got.Name != "Smoke Native" || got.ContextWindow != 32768 || !got.CanReason || got.PriceInput != 0.25 {
		t.Fatalf("custom model metadata = %+v", got)
	}
	if got := reg.ModelIDs(); len(got) != 1 || got[0] != "smoke-native/smoke-model" {
		t.Fatalf("ModelIDs = %v, want one deduplicated qualified ID", got)
	}
	if err := reg.CheckModel("smoke-native/smoke-model"); err != nil {
		t.Fatalf("qualified custom model rejected: %v", err)
	}
	if got := reg.APIModelID("smoke-native/smoke-model"); got != "smoke-model" {
		t.Fatalf("APIModelID = %q, want bare provider API ID", got)
	}
}

func TestQualifiedConfigModelOverridesCatalogMetadata(t *testing.T) {
	catalogModel := CatalogModel{Name: "Catalog"}
	catalogModel.Limit.Context = 1024
	catalogModel.Cost.Input = 9
	cfg := &config.Config{
		Providers: []config.Provider{{
			Name: "custom", Type: "openai-compat", BaseURL: "https://custom.test/v1",
		}},
		Models: []config.Model{{
			ID: "custom/reasoner", Name: "Configured", ContextWindow: 654321,
			CanReason: true, PriceInput: 1.5,
		}},
	}
	catalog := map[string]CatalogProvider{
		"custom": {
			ID: "custom", API: "https://catalog.test/v1",
			Models: map[string]CatalogModel{"reasoner": catalogModel},
		},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, catalog)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	models, err := reg.Models(context.Background(), "custom")
	if err != nil || len(models) != 1 {
		t.Fatalf("Models = %+v, %v", models, err)
	}
	if got := models[0]; got.ID != "reasoner" || got.Name != "Configured" || got.ContextWindow != 654321 || got.PriceInput != 1.5 || !got.CanReason {
		t.Fatalf("explicit metadata did not override catalog: %+v", got)
	}
}

func TestBareConfigModelWithMultipleProvidersIsExplicitlyAmbiguous(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "one", Type: "openai-compat", BaseURL: "https://one.test/v1"},
			{Name: "two", Type: "openai-compat", BaseURL: "https://two.test/v1"},
		},
		Models: []config.Model{{ID: "shared-model"}},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, map[string]CatalogProvider{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if provider, ok := reg.ProviderOf("shared-model"); ok || provider != "" {
		t.Fatalf("ambiguous bare model resolved to %q, ok=%v", provider, ok)
	}
	if model, ok := reg.Model("shared-model"); ok || model.ID != "" {
		t.Fatalf("ambiguous bare config metadata resolved: %+v, ok=%v", model, ok)
	}
	if metadata, ok := reg.ModelMetadata("shared-model"); !ok || metadata.ID != "shared-model" {
		t.Fatalf("read-only configured metadata = %+v, ok=%v", metadata, ok)
	}
	err = reg.CheckModel("shared-model")
	if err == nil || !strings.Contains(err.Error(), "ambiguous provider binding") || !strings.Contains(err.Error(), "provider/shared-model") {
		t.Fatalf("CheckModel ambiguity error = %v", err)
	}
}

func TestBareConfigModelCountsCustomDiscoverableProviderAsAmbiguous(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "one", Type: "openai-compat", BaseURL: "https://one.test/v1"},
			{Name: "two", Type: "openai-compat", BaseURL: "https://two.test/v1", DiscoverModels: true},
		},
		Models: []config.Model{{ID: "shared-model"}},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, map[string]CatalogProvider{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if provider, ok := reg.ProviderOf("shared-model"); ok || provider != "" {
		t.Fatalf("bare model routed to %q despite discoverable second provider", provider)
	}
	if err := reg.CheckModel("shared-model"); err == nil || !strings.Contains(err.Error(), "ambiguous provider binding across one, two") {
		t.Fatalf("CheckModel ambiguity error = %v", err)
	}
}

func TestBareModelAdvertisedByMultipleProvidersIsAmbiguous(t *testing.T) {
	catalogModel := CatalogModel{Name: "Shared"}
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "one", Type: "openai-compat", BaseURL: "https://one.test/v1"},
		{Name: "two", Type: "openai-compat", BaseURL: "https://two.test/v1"},
	}}
	catalog := map[string]CatalogProvider{
		"one": {ID: "one", API: "https://one.test/v1", Models: map[string]CatalogModel{"shared-model": catalogModel}},
		"two": {ID: "two", API: "https://two.test/v1", Models: map[string]CatalogModel{"shared-model": catalogModel}},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, catalog)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if provider, ok := reg.ProviderOf("shared-model"); ok || provider != "" {
		t.Fatalf("duplicate bare catalog model resolved to %q, ok=%v", provider, ok)
	}
	if model, ok := reg.Model("shared-model"); ok || model.ID != "" {
		t.Fatalf("duplicate bare model metadata resolved nondeterministically: %+v, ok=%v", model, ok)
	}
	err = reg.CheckModel("shared-model")
	if err == nil || !strings.Contains(err.Error(), "ambiguous across providers one, two") || !strings.Contains(err.Error(), "one/shared-model") {
		t.Fatalf("CheckModel duplicate-provider error = %v", err)
	}
}

func TestNamespacedAPIModelUsesProviderQualifiedSelectionHandle(t *testing.T) {
	const (
		provider = "fireworks"
		apiID    = "accounts/fireworks/models/deepseek-v3"
		handle   = provider + "/" + apiID
	)
	catalogModel := CatalogModel{Name: "DeepSeek V3"}
	cfg := &config.Config{Providers: []config.Provider{{
		Name: provider, Type: "openai-compat", BaseURL: "https://api.fireworks.ai/inference/v1",
	}}}
	catalog := map[string]CatalogProvider{
		provider: {
			ID: provider, API: "https://api.fireworks.ai/inference/v1",
			Models: map[string]CatalogModel{apiID: catalogModel},
		},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, catalog)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	if got := reg.ModelIDs(); len(got) != 1 || got[0] != handle {
		t.Fatalf("ModelIDs = %v, want %q", got, handle)
	}
	if providerName, ok := reg.ProviderOf(handle); !ok || providerName != provider {
		t.Fatalf("ProviderOf(%q) = %q, %v", handle, providerName, ok)
	}
	if model, ok := reg.Model(handle); !ok || model.ID != apiID {
		t.Fatalf("Model(%q) = %+v, %v; want raw API ID %q", handle, model, ok, apiID)
	}
	if got := reg.APIModelID(handle); got != apiID {
		t.Fatalf("APIModelID(%q) = %q, want %q", handle, got, apiID)
	}
	if err := reg.CheckModel(handle); err != nil {
		t.Fatalf("CheckModel(%q): %v", handle, err)
	}
}

func TestQualifiedModelWithMissingOrDisabledProviderNeverFallsBack(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{
			{Name: "one", Type: "openai-compat", BaseURL: "https://one.test/v1", Disabled: true},
			{Name: "two", Type: "openai-compat", BaseURL: "https://two.test/v1"},
		},
		Models: []config.Model{{ID: "one/shared"}},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, map[string]CatalogProvider{})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if provider, ok := reg.ProviderOf("one/shared"); ok || provider != "" {
		t.Fatalf("missing qualified provider fell back to %q", provider)
	}
	if model, ok := reg.Model("one/shared"); ok || model.ID != "" {
		t.Fatalf("model resolved without its qualified provider: %+v, ok=%v", model, ok)
	}
	if metadata, ok := reg.ModelMetadata("one/shared"); !ok || metadata.ID != "one/shared" {
		t.Fatalf("read-only configured metadata = %+v, ok=%v", metadata, ok)
	}
	if got := reg.ModelIDs(); len(got) != 0 {
		t.Fatalf("unroutable qualified model remained selectable: %v", got)
	}
	if err := reg.CheckModel("one/shared"); err == nil || !strings.Contains(err.Error(), `provider "one"`) || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("CheckModel missing-provider error = %v", err)
	}
}

func TestRegistryRejectsInvalidConfiguredAndCatalogProviderIDs(t *testing.T) {
	cfg := &config.Config{Providers: []config.Provider{
		{Name: "bad/name", Type: "openai-compat", BaseURL: "https://bad.test/v1"},
		{Name: "valid-provider", Type: "openai-compat", BaseURL: "https://valid.test/v1"},
	}}
	catalog := map[string]CatalogProvider{
		"remote\nprovider": {
			ID: "remote\nprovider", API: "https://remote.test/v1",
			Models: map[string]CatalogModel{"model": {Name: "Model"}},
		},
	}
	reg, err := NewRegistry(context.Background(), cfg, nil, catalog)
	if err == nil || !strings.Contains(err.Error(), "provider name") {
		t.Fatalf("NewRegistry error = %v, want invalid provider diagnostic", err)
	}
	if _, ok := reg.Provider("bad/name"); ok {
		t.Fatal("invalid configured provider was registered")
	}
	if _, ok := reg.Provider("remote\nprovider"); ok {
		t.Fatal("invalid catalog provider was registered")
	}
	if _, ok := reg.Catalog()["remote\nprovider"]; ok {
		t.Fatal("invalid catalog provider remained visible")
	}
	if _, ok := reg.Provider("valid-provider"); !ok {
		t.Fatal("valid provider was dropped with invalid entries")
	}
	for _, id := range reg.ModelIDs() {
		if strings.Contains(id, "remote") || strings.Contains(id, "bad/name") {
			t.Fatalf("invalid provider produced model handle %q", id)
		}
	}
}
