package agentcore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/config"
)

func TestCoreCatalog(t *testing.T) {
	catalog, err := coreCatalog()
	if err != nil {
		t.Fatalf("coreCatalog: %v", err)
	}
	for _, want := range []string{"openai", "anthropic", "google", "deepseek", "groq", "openrouter", "xai", "ollama", "opencode-go"} {
		if _, ok := catalog[want]; !ok {
			t.Errorf("core catalog missing %s", want)
		}
	}
	openai := catalog["openai"]
	if len(openai.Env) != 1 || openai.Env[0] != "OPENAI_API_KEY" {
		t.Errorf("openai env = %v", openai.Env)
	}
	if catalogType(openai) != "openai-compat" {
		t.Errorf("openai type = %s", catalogType(openai))
	}
	if catalogType(catalog["anthropic"]) != "anthropic" {
		t.Errorf("anthropic type = %s", catalogType(catalog["anthropic"]))
	}
	models := catalogModels(openai)
	if len(models) < 5 {
		t.Fatalf("openai models = %d", len(models))
	}
	var gpt5 *Model
	for i := range models {
		if models[i].ID == "gpt-5" {
			gpt5 = &models[i]
		}
	}
	if gpt5 == nil || gpt5.PriceInput != 1.25 || gpt5.PriceOutput != 10 || !gpt5.CanReason {
		t.Fatalf("gpt-5 = %+v", gpt5)
	}
	if !isLocalProvider(catalog["ollama"]) || isLocalProvider(catalog["openai"]) {
		t.Error("local detection wrong")
	}
}

func TestParseCatalogRemoteShape(t *testing.T) {
	data := `{"openai":{"id":"openai","name":"OpenAI","env":["OPENAI_API_KEY"],"npm":"@ai-sdk/openai","api":"https://api.openai.com/v1","models":{"gpt-5":{"id":"gpt-5","name":"GPT-5","reasoning":true,"cost":{"input":1.25,"output":10,"cache_read":0.125,"cache_write":1.25},"limit":{"context":400000,"output":100000},"modalities":{"input":["text","image"],"output":["text"]},"status":"active"}}}}`
	catalog, err := ParseCatalog([]byte(data))
	if err != nil {
		t.Fatalf("ParseCatalog: %v", err)
	}
	if len(catalog) != 1 {
		t.Fatalf("providers = %d", len(catalog))
	}
	models := catalogModels(catalog["openai"])
	if len(models) != 1 || models[0].ContextWindow != 400000 || !models[0].CanReason {
		t.Fatalf("models = %+v", models)
	}
}

func TestModelsDevCacheThenRemoteThenCore(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "models.json")
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"openai":{"id":"openai","name":"OpenAI","env":["OPENAI_API_KEY"],"npm":"@ai-sdk/openai","api":"https://api.openai.com/v1","models":{"gpt-5":{"id":"gpt-5","name":"GPT-5","cost":{"input":1.25,"output":10},"limit":{"context":400000,"output":100000},"status":"active"}}}}`))
	}))
	defer srv.Close()

	dev := NewModelsDev(ModelsDevOptions{URL: srv.URL, CachePath: cache, TTL: time.Hour})
	ctx := context.Background()

	// First load: remote.
	providers, source, err := dev.Load(ctx)
	if err != nil || source != "remote" {
		t.Fatalf("first load = %v, %q", err, source)
	}
	if _, ok := providers["openai"]; !ok {
		t.Fatal("remote provider missing")
	}
	// Cache written.
	if _, err := os.Stat(cache); err != nil {
		t.Fatalf("cache missing: %v", err)
	}
	// Second load: from cache, no network.
	_, source, err = dev.Load(ctx)
	if err != nil || source != "cache" {
		t.Fatalf("cached load = %v, %q", err, source)
	}
	if hits != 1 {
		t.Errorf("network hits = %d, want 1", hits)
	}
	// Disabled: core snapshot, no network.
	dev2 := NewModelsDev(ModelsDevOptions{URL: srv.URL, CachePath: cache, Disabled: true})
	providers, source, err = dev2.Load(ctx)
	if err != nil || source != "core" {
		t.Fatalf("disabled load = %v, %q", err, source)
	}
	if _, ok := providers["anthropic"]; !ok {
		t.Error("core fallback missing anthropic")
	}
}

func TestModelsDevStaleCacheFallback(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "models.json")
	// Write a stale cache (older than TTL).
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(cache, []byte(`{"openai":{"id":"openai","name":"OpenAI","env":["OPENAI_API_KEY"],"npm":"@ai-sdk/openai","api":"https://api.openai.com/v1","models":{"gpt-4o":{"id":"gpt-4o","name":"GPT-4o","cost":{"input":2.5,"output":10},"limit":{"context":128000,"output":16384},"status":"active"}}}}`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(cache, past, past)

	// Remote is down → stale cache is used as the best available.
	dev := NewModelsDev(ModelsDevOptions{URL: "http://127.0.0.1:1/x", CachePath: cache, TTL: 5 * time.Minute})
	providers, source, err := dev.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if source != "cache" {
		t.Errorf("source = %q, want cache", source)
	}
	if _, ok := providers["openai"]; !ok {
		t.Error("stale cache provider missing")
	}
}

func TestRegistryEnvAutoDetection(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-env")
	cfg := &config.Config{} // no maestrorc providers at all
	keys := mapKeyStore{}
	ctx := context.Background()
	r, err := NewRegistry(ctx, cfg, keys, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	// OpenAI auto-registered from env + catalog; locals always present.
	if _, ok := r.Provider("openai"); !ok {
		t.Fatal("openai not auto-registered from env")
	}
	p, _ := r.Provider("openai")
	models := p.Models()
	if len(models) < 5 {
		t.Errorf("openai catalog models = %d", len(models))
	}
	// A provider without a key is NOT auto-registered.
	if _, ok := r.Provider("anthropic"); ok {
		t.Error("anthropic should not register without a key")
	}
}

func TestRegistryPassesCatalogDeclaredEnvironmentKeyToProvider(t *testing.T) {
	core, err := coreCatalog()
	if err != nil {
		t.Fatalf("coreCatalog: %v", err)
	}
	for _, tc := range []struct {
		provider string
		env      string
	}{
		{provider: "google", env: "GEMINI_API_KEY"},
		{provider: "opencode-go", env: "OPENCODE_API_KEY"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			const secret = "catalog-declared-secret"
			t.Setenv(tc.env, secret)
			catalog := map[string]CatalogProvider{tc.provider: core[tc.provider]}
			r, err := NewRegistry(context.Background(), &config.Config{}, mapKeyStore{}, catalog)
			if err != nil {
				t.Fatalf("NewRegistry: %v", err)
			}
			provider, ok := r.Provider(tc.provider)
			if !ok {
				t.Fatalf("%s was not detected from %s", tc.provider, tc.env)
			}
			openai, ok := provider.(*openaiProvider)
			if !ok {
				t.Fatalf("provider type = %T, want *openaiProvider", provider)
			}
			if openai.apiKey != secret {
				t.Fatalf("%s did not receive the credential declared by catalog env", tc.provider)
			}
		})
	}
}

func TestRegistryEnvOverCatalogMetadata(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "sk-ds")
	cfg := &config.Config{
		Providers: []config.Provider{{Name: "deepseek", Type: "openai-compat", BaseURL: "http://custom:1234/v1"}},
	}
	r, err := NewRegistry(context.Background(), cfg, mapKeyStore{}, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	p, _ := r.Provider("deepseek")
	// maestrorc wins for config; the catalog still provides model metadata.
	models := p.Models()
	if len(models) != 2 {
		t.Errorf("deepseek catalog models = %d, want 2", len(models))
	}
	var chat *Model
	for i := range models {
		if models[i].ID == "deepseek-chat" {
			chat = &models[i]
		}
	}
	if chat == nil || chat.PriceInput != 0.27 {
		t.Fatalf("deepseek-chat pricing = %+v", chat)
	}
	_ = strings.TrimSpace // keep import
}

func TestEnsureLocalsMergedIntoRemote(t *testing.T) {
	// Simulate a remote catalog without the local providers.
	remote := map[string]CatalogProvider{
		"openai": {ID: "openai", Name: "OpenAI", Env: []string{"OPENAI_API_KEY"}, NPM: "@ai-sdk/openai", API: "https://api.openai.com/v1", Models: map[string]CatalogModel{}},
	}
	ensureLocals(remote)
	for _, id := range []string{"ollama", "llamacpp", "lmstudio", "litellm"} {
		if _, ok := remote[id]; !ok {
			t.Errorf("local %s missing after ensureLocals", id)
		}
	}
	// The remote entry is untouched.
	if _, ok := remote["openai"]; !ok {
		t.Error("remote entry lost")
	}
}
