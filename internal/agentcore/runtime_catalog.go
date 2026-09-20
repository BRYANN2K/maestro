package agentcore

import (
	"context"
	_ "embed"
	"encoding/json"
	"slices"
)

//go:embed data/runtime-models.json
var runtimeCatalogJSON []byte

var runtimeCatalog = func() map[string][]Model {
	var catalog map[string][]Model
	if err := json.Unmarshal(runtimeCatalogJSON, &catalog); err != nil {
		panic(err)
	}
	return catalog
}()

func AccountProviders() []string {
	return []string{"openai-codex", "anthropic", "google-gemini-cli", "github-copilot"}
}

func IsRuntimeCredential(key string) bool {
	var value struct {
		OAuth json.RawMessage `json:"maestro_oauth"`
	}
	return json.Unmarshal([]byte(key), &value) == nil && len(value.OAuth) > 0 && string(value.OAuth) != "null"
}

func newRuntimeProvider(name, url, key string, keys KeyStore, overrides []Model) *RuntimeProvider {
	if key == "" && keys != nil {
		key, _ = keys.Key(name)
	}
	models := make([]Model, len(runtimeCatalog[name]))
	copy(models, runtimeCatalog[name])
	for i := range models {
		models[i].Efforts = slices.Clone(models[i].Efforts)
	}
	p := &RuntimeProvider{ProviderName: name, BaseURL: url, Key: key, Static: overrideModels(models, overrides)}
	if writer, ok := keys.(interface {
		SaveKey(context.Context, string, string) error
	}); ok {
		p.SaveCredential = writer.SaveKey
	}
	return p
}
