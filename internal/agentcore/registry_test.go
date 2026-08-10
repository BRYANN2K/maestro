package agentcore

import (
	"context"
	"strings"
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
