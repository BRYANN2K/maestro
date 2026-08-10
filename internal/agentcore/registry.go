package agentcore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/bryann2k/maestro/internal/config"
)

// KeyStore resolves API keys for provider names. Implementations: vault,
// environment, or a test double.
type KeyStore interface {
	Key(name string) (string, bool)
}

// Registry builds providers from maestrorc config plus the provider
// catalog, and answers model lookups across them. Resolution order:
// maestrorc > environment-detected catalog > local providers (§10.3).
type Registry struct {
	providers map[string]Provider
	models    map[string]Model // model ID → model, deduplicated
	catalog   map[string]CatalogProvider
	envKeys   func(string) (string, bool)
}

// NewRegistry builds a provider per config.Provider entry, then auto-
// registers catalog providers whose env key is present, plus the local
// providers (ollama, llamacpp, lmstudio, litellm). catalog may be nil —
// then the embedded core snapshot is used.
func NewRegistry(ctx context.Context, cfg *config.Config, keys KeyStore, catalog map[string]CatalogProvider) (*Registry, error) {
	r := &Registry{providers: map[string]Provider{}, models: map[string]Model{}}
	if catalog == nil {
		var err error
		catalog, err = coreCatalog()
		if err != nil {
			return nil, err
		}
	}
	ensureLocals(catalog)
	var errs []error
	validatedCatalog := make(map[string]CatalogProvider, len(catalog))
	for id, provider := range catalog {
		if err := config.ValidateProviderID(id); err != nil {
			errs = append(errs, fmt.Errorf("catalog provider %q: %w", id, err))
			continue
		}
		if provider.ID == "" {
			provider.ID = id
		} else if err := config.ValidateProviderID(provider.ID); err != nil {
			errs = append(errs, fmt.Errorf("catalog provider %q metadata: %w", id, err))
			continue
		} else if provider.ID != id {
			errs = append(errs, fmt.Errorf("catalog provider %q metadata ID %q does not match", id, provider.ID))
			continue
		}
		validatedCatalog[id] = provider
	}
	catalog = validatedCatalog
	r.catalog = catalog
	r.envKeys = func(name string) (string, bool) {
		if keys != nil {
			return keys.Key(name)
		}
		return "", false
	}
	// 1. maestrorc wins on name collision; catalog metadata (models,
	// pricing) is inherited when the provider matches a catalog entry.
	for _, p := range cfg.Providers {
		if err := config.ValidateProviderID(p.Name); err != nil {
			errs = append(errs, fmt.Errorf("configured provider: %w", err))
			continue
		}
		if p.Disabled {
			continue
		}
		if err := config.ValidateProvider(p); err != nil {
			errs = append(errs, err)
			continue
		}
		static := staticModelsFor(p.Name)
		if len(static) == 0 {
			if cp, ok := catalog[p.Name]; ok {
				static = catalogModels(cp)
			}
		}
		// A qualified `model add provider/model` belongs to that provider.
		// Attach it to the provider's static model set so discovery, picker
		// listings, pricing, and the request wire all agree on the same bare
		// API model ID. Unqualified entries are deliberately left unresolved:
		// attributing one while several providers exist would be unsafe.
		static = overrideModels(static, configuredModelsForProvider(cfg.Models, p.Name))
		prov, err := buildProvider(ctx, p, keys, static)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r.providers[p.Name] = prov
	}

	// 2. Catalog providers detected via their env var (or local).
	for id, cp := range catalog {
		if _, configured := r.providers[id]; configured {
			continue // maestrorc wins
		}
		if !isLocalProvider(cp) {
			if _, ok := r.envKeys(id); !ok && !catalogEnvPresent(cp) {
				continue // no key → skip (reported by auth status)
			}
		}
		prov, err := buildCatalogProvider(ctx, id, cp, keys, configuredModelsForProvider(cfg.Models, id))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		r.providers[id] = prov
	}

	// 3. Explicit model add entries override catalog metadata.
	for _, m := range cfg.Models {
		r.models[m.ID] = modelFromConfig(m, m.ID)
	}
	return r, errors.Join(errs...)
}

// configuredModelsForProvider returns only explicit models whose first path
// component names provider. The returned ID is the exact API model ID after
// removing the Maestro provider-selection prefix. A remaining slash is kept
// (for example provider/accounts/acme/model -> accounts/acme/model).
func configuredModelsForProvider(models []config.Model, provider string) []Model {
	var out []Model
	for _, configured := range models {
		prefix, apiID, qualified := strings.Cut(configured.ID, "/")
		if !qualified || prefix != provider || apiID == "" {
			continue
		}
		out = append(out, modelFromConfig(configured, apiID))
	}
	return out
}

func modelFromConfig(model config.Model, id string) Model {
	return Model{
		ID:               id,
		Name:             model.Name,
		ContextWindow:    model.ContextWindow,
		DefaultMaxTokens: model.DefaultMaxTokens,
		CanReason:        model.CanReason,
		SupportsImages:   model.SupportsImages,
		PriceInput:       model.PriceInput,
		PriceOutput:      model.PriceOutput,
		PriceCacheCreate: model.PriceCacheCreate,
		PriceCacheHit:    model.PriceCacheHit,
		ReasoningEffort:  model.ReasoningEffort,
	}
}

// overrideModels preserves the catalog order while making explicit
// maestrorc metadata authoritative. Newly configured IDs are appended in
// declaration order.
func overrideModels(base, overrides []Model) []Model {
	out := append([]Model(nil), base...)
	positions := make(map[string]int, len(out))
	for i, model := range out {
		positions[model.ID] = i
	}
	for _, model := range overrides {
		if i, ok := positions[model.ID]; ok {
			out[i] = model
			continue
		}
		positions[model.ID] = len(out)
		out = append(out, model)
	}
	return out
}

// ensureLocals guarantees the local providers exist in the catalog — the
// remote models.dev catalog does not carry ollama/llamacpp/litellm, but
// they are always usable.
func ensureLocals(catalog map[string]CatalogProvider) {
	core, err := coreCatalog()
	if err != nil {
		return
	}
	for _, id := range []string{"ollama", "llamacpp", "lmstudio", "litellm"} {
		if _, ok := catalog[id]; !ok {
			if cp, ok := core[id]; ok {
				catalog[id] = cp
			}
		}
	}
}

// catalogEnvPresent reports whether any of the provider's catalog env vars
// is set in the process environment.
func catalogEnvPresent(cp CatalogProvider) bool {
	_, ok := catalogEnvKey(cp)
	return ok
}

// catalogEnvKey resolves the exact environment variable names declared by
// models.dev. Provider IDs are not always a mechanical match for their key:
// google uses GEMINI_API_KEY and opencode-go uses OPENCODE_API_KEY.
func catalogEnvKey(cp CatalogProvider) (string, bool) {
	for _, env := range cp.Env {
		if key := os.Getenv(env); key != "" {
			return key, true
		}
	}
	return "", false
}

// buildCatalogProvider constructs a provider from catalog metadata.
func buildCatalogProvider(ctx context.Context, id string, cp CatalogProvider, keys KeyStore, configured []Model) (Provider, error) {
	typ := catalogType(cp)
	baseURL := cp.API
	key := ""
	if keys != nil {
		key, _ = keys.Key(id)
	}
	if key == "" {
		key, _ = catalogEnvKey(cp)
	}
	catalogStatic := catalogModels(cp)
	discover := len(catalogStatic) == 0 // local providers and empty catalogs discover live
	static := overrideModels(catalogStatic, configured)
	switch typ {
	case "anthropic":
		return newAnthropic(id, baseURL, key, static), nil
	default:
		return newOpenAIWithType(id, typ, baseURL, key, discover, nil, static), nil
	}
}

func buildProvider(ctx context.Context, p config.Provider, keys KeyStore, static []Model) (Provider, error) {
	switch p.Type {
	case "openai", "openai-compat", "ollama", "llamacpp", "lmstudio", "litellm":
		key := p.APIKey
		if key == "" && keys != nil {
			key, _ = keys.Key(p.Name)
		}
		if p.BaseURL == "" {
			return nil, fmt.Errorf("provider %s: --base-url is required for type %s", p.Name, p.Type)
		}
		return newOpenAIWithType(p.Name, p.Type, p.BaseURL, key, p.DiscoverModels, p.ExtraHeaders, static), nil
	case "anthropic":
		key := p.APIKey
		if key == "" && keys != nil {
			key, _ = keys.Key(p.Name)
		}
		return newAnthropic(p.Name, p.BaseURL, key, static), nil
	case "anthropic-oauth", "openai-codex-oauth", "gemini-oauth", "perplexity-oauth":
		return nil, fmt.Errorf("provider %s: OAuth providers are not supported until B9", p.Name)
	case "bedrock", "vertexai":
		return nil, fmt.Errorf("provider %s: %s is not supported yet (B9+)", p.Name, p.Type)
	default:
		return nil, fmt.Errorf("provider %s: unknown type %q", p.Name, p.Type)
	}
}

// staticModelsFor maps a provider name to its built-in static model list.
// The authoritative catalog arrives via `model add` config entries.
func staticModelsFor(name string) []Model { return nil }

// SlotsFromConfig converts maestrorc slots and modelRoles into the
// agentcore types used by ResolveRole.
func SlotsFromConfig(cfg *config.Config) (slots, roles map[string]Slot) {
	slots = map[string]Slot{}
	for name, s := range cfg.Slots {
		slots[name] = SlotFromConfig(s)
	}
	roles = map[string]Slot{}
	for name, s := range cfg.ModelRoles {
		roles[name] = SlotFromConfig(s)
	}
	return slots, roles
}

// SlotFromConfig converts one config slot into an agentcore slot.
func SlotFromConfig(s config.Slot) Slot {
	return Slot{Model: s.Model, Sampling: SamplingFromConfig(s.Sampling)}
}

// SamplingFromConfig converts config sampling flags into agentcore sampling.
func SamplingFromConfig(s config.Sampling) Sampling {
	return Sampling{
		Think:            s.Think,
		ReasoningEffort:  s.ReasoningEffort,
		MaxTokens:        s.MaxTokens,
		Temperature:      s.Temperature,
		TopP:             s.TopP,
		TopK:             s.TopK,
		FrequencyPenalty: s.FrequencyPenalty,
		PresencePenalty:  s.PresencePenalty,
	}
}

// Catalog returns the provider catalog backing the registry.
func (r *Registry) Catalog() map[string]CatalogProvider {
	return r.catalog
}

// Provider returns the provider registered under name.
func (r *Registry) Provider(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// ReasoningEfforts returns only effort values honored by the model's actual
// provider wire protocol. Provider display names are never used as a proxy
// for capabilities, so a custom Anthropic/OpenAI provider behaves correctly.
func (r *Registry) ReasoningEfforts(modelID string) []string {
	providerName, ok := r.ProviderOf(modelID)
	if !ok {
		if name, candidate, cut := strings.Cut(modelID, "/"); cut {
			if catalogProvider, exists := r.catalog[name]; exists {
				catalogModel, known := catalogProvider.Models[candidate]
				return ReasoningEffortsForProvider(catalogType(catalogProvider), candidate, known && catalogModel.Reasoning)
			}
		}
		return append([]string(nil), automaticEfforts...)
	}
	provider, ok := r.Provider(providerName)
	if !ok {
		return append([]string(nil), automaticEfforts...)
	}
	model, known := r.Model(modelID)
	canReason := known && model.CanReason
	apiModel := r.APIModelID(modelID)
	return ReasoningEffortsForProvider(provider.Type(), apiModel, canReason)
}

// Providers returns the registered provider names, sorted.
func (r *Registry) Providers() []string {
	names := make([]string, 0, len(r.providers))
	for n := range r.providers {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Models returns the models of one provider: static + discovered.
func (r *Registry) Models(ctx context.Context, provider string) ([]Model, error) {
	p, ok := r.providers[provider]
	if !ok {
		return nil, fmt.Errorf("provider %s not configured", provider)
	}
	return p.Models(), nil
}

// Model finds a model by its fully qualified ID ("provider/model") or by a
// bare ID across all providers.
func (r *Registry) Model(id string) (Model, bool) {
	if provider, _, qualified := strings.Cut(id, "/"); qualified {
		if _, active := r.providers[provider]; !active {
			return Model{}, false
		}
	}
	if m, ok := r.models[id]; ok {
		if !strings.Contains(id, "/") {
			if _, resolved := r.ProviderOf(id); !resolved {
				return Model{}, false
			}
		}
		return m, true
	}
	if provider, model, ok := strings.Cut(id, "/"); ok {
		if p, found := r.providers[provider]; found {
			// Legacy unqualified config entries are exposed through a qualified
			// selection handle once their sole provider is known.
			if configured, exists := r.models[model]; exists {
				if owner, resolved := r.ProviderOf(model); resolved && owner == provider {
					return configured, true
				}
			}
			for _, m := range p.Models() {
				if m.ID == id || m.ID == model {
					return m, true
				}
			}
		}
	}
	var candidate Model
	found := false
	for _, p := range r.providers {
		for _, m := range p.Models() {
			if m.ID == id {
				if found {
					return Model{}, false
				}
				candidate = m
				found = true
				break
			}
		}
	}
	return candidate, found
}

// ModelMetadata returns descriptive metadata for a model without asserting
// that the model currently has a safe provider route. This is intentionally
// narrower than Model: read-only consumers such as context-window displays
// may describe a configured legacy/subscription model, while execution must
// continue to use ProviderOf, CheckModel, or Model and therefore fail closed
// for ambiguous, missing, or disabled providers.
func (r *Registry) ModelMetadata(id string) (Model, bool) {
	if model, configured := r.models[id]; configured {
		return model, true
	}
	return r.Model(id)
}

// ProviderOf returns the provider name that serves modelID, resolving both
// qualified ("provider/model") and bare IDs.
func (r *Registry) ProviderOf(modelID string) (string, bool) {
	if provider, _, ok := strings.Cut(modelID, "/"); ok {
		if _, found := r.providers[provider]; found {
			return provider, true
		}
		// Slash-bearing values are explicit selection handles. Never reinterpret
		// a missing/disabled provider prefix as a legacy bare model and route it
		// through whichever unrelated provider happens to be active.
		return "", false
	}
	// An explicit model without a provider-selection prefix must never be
	// attributed merely because one provider catalog happens to contain the
	// same ID. Preserve the legacy sole-provider fallback, but fail closed as
	// soon as more than one non-local provider could own it.
	if _, ok := r.models[modelID]; ok {
		candidates := r.configModelProviderCandidates()
		if len(candidates) == 1 {
			return candidates[0], true
		}
		return "", false
	}
	matches := r.modelProviderMatches(modelID)
	if len(matches) == 1 {
		return matches[0], true
	}
	return "", false
}

// configModelProviderCandidates returns the providers eligible for the
// legacy unqualified `model add X` fallback. Built-in local transports are
// excluded because they are auto-registered and do not own remote config
// entries. A custom provider with live discovery remains a candidate: an
// empty or changing model list is not evidence that it cannot own the ID.
// Sorting makes diagnostics and tests deterministic.
func (r *Registry) configModelProviderCandidates() []string {
	var candidates []string
	for name, provider := range r.providers {
		switch name {
		case "ollama", "llamacpp", "lmstudio", "litellm":
			continue
		}
		switch provider.Type() {
		case "ollama", "llamacpp", "lmstudio", "litellm":
			continue
		}
		candidates = append(candidates, name)
	}
	slices.Sort(candidates)
	return candidates
}

// modelProviderMatches returns every provider that advertises modelID.
// Bare IDs are usable only when this set is unique; returning the first map
// match would route prompts and credentials nondeterministically.
func (r *Registry) modelProviderMatches(modelID string) []string {
	var matches []string
	for name, provider := range r.providers {
		for _, model := range provider.Models() {
			if model.ID == modelID {
				matches = append(matches, name)
				break
			}
		}
	}
	slices.Sort(matches)
	return matches
}

// catalogServes reports whether the live models.dev catalog (not the
// provider's static snapshot, which is frozen at registration time) lists
// the model for the provider.
func (r *Registry) catalogServes(providerName, modelID string) bool {
	cp, ok := r.catalog[providerName]
	if !ok {
		return false
	}
	bare := modelID
	if _, m, ok := strings.Cut(modelID, "/"); ok {
		bare = m
	}
	_, ok = cp.Models[bare]
	return ok
}

// APIModelID maps a selection handle to the model id sent to the provider
// API — the equivalent of opencode's api.id and the old maestro's bare
// settings.Provider.Model:
//
//   - "provider/model" whose provider serves the bare catalog id
//     (e.g. "opencode/deepseek-v4-flash-free") → "deepseek-v4-flash-free"
//   - genuinely-qualified ids (e.g. "accounts/fireworks/models/x") → as-is
//   - bare config entries → as-is
func (r *Registry) APIModelID(modelID string) string {
	if provider, bare, cut := strings.Cut(modelID, "/"); cut && bare != "" {
		// An exact qualified maestrorc entry is a Maestro selection handle.
		// Its provider prefix never belongs on the provider wire; any further
		// namespace in bare remains intact.
		if _, configured := r.models[modelID]; configured {
			if _, exists := r.providers[provider]; exists {
				return bare
			}
		}
		if configured, exists := r.models[bare]; exists {
			if owner, resolved := r.ProviderOf(bare); resolved && owner == provider {
				return configured.ID
			}
		}
		if p, ok := r.providers[provider]; ok {
			for _, m := range p.Models() {
				if m.ID == bare {
					return m.ID
				}
			}
		}
		if cp, ok := r.catalog[provider]; ok {
			if _, ok := cp.Models[bare]; ok {
				return bare
			}
		}
	}
	if m, ok := r.models[modelID]; ok && m.ID != "" {
		return m.ID
	}
	return modelID
}

// Discoverable marks providers whose model list is fetched live (ollama and
// friends): their model list may be empty until the first fetch, so unknown
// model IDs must not be rejected preflight.
type Discoverable interface {
	Discoverable() bool
}

// ModelIDs returns every known model ID: provider models (qualified) plus
// explicit model-add entries from the config.
func (r *Registry) ModelIDs() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range r.Providers() {
		for _, m := range r.modelsFor(name) {
			if m.ID == "" {
				continue
			}
			// Provider.Models always returns raw API IDs. A slash inside that
			// raw ID is a provider namespace, not a Maestro selection handle.
			id := name + "/" + m.ID
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	for id := range r.models {
		provider, resolved := r.ProviderOf(id)
		if !resolved {
			continue // ambiguous/unbound config entries are not selectable
		}
		handle := id
		if prefix, _, qualified := strings.Cut(id, "/"); !qualified || prefix != provider {
			handle = provider + "/" + id
		}
		if !seen[handle] {
			seen[handle] = true
			out = append(out, handle)
		}
	}
	slices.Sort(out)
	return out
}

// modelsFor returns a provider's served models.
func (r *Registry) modelsFor(name string) []Model {
	if p, ok := r.providers[name]; ok {
		return p.Models()
	}
	return nil
}

// CheckModel preflights a model before a run: the provider must exist and,
// for static/catalog providers, the model must be served — either by the
// provider's frozen snapshot or by the live models.dev catalog (a model
// released between refreshes stays selectable). Live-discovery providers
// are exempt. Fails fast with a clean message instead of a raw provider
// error dump mid-turn.
func (r *Registry) CheckModel(modelID string) error {
	providerName, ok := r.ProviderOf(modelID)
	if !ok {
		if provider, _, qualified := strings.Cut(modelID, "/"); qualified {
			return fmt.Errorf("model %q not available — provider %q is not configured", modelID, provider)
		}
		if _, configured := r.models[modelID]; configured {
			candidates := r.configModelProviderCandidates()
			if len(candidates) > 1 {
				return fmt.Errorf("model %q has an ambiguous provider binding across %s — declare and select it as provider/%s", modelID, strings.Join(candidates, ", "), modelID)
			}
			return fmt.Errorf("model %q has no provider binding — declare and select it as provider/%s", modelID, modelID)
		}
		if matches := r.modelProviderMatches(modelID); len(matches) > 1 {
			return fmt.Errorf("model %q is ambiguous across providers %s — select a qualified ID such as %s/%s", modelID, strings.Join(matches, ", "), matches[0], modelID)
		}
		return fmt.Errorf("model %q not available — run 'maestro model list'", modelID)
	}
	if _, known := r.Model(modelID); known || r.catalogServes(providerName, modelID) {
		return nil
	}
	if p, ok := r.providers[providerName]; ok {
		if d, ok := p.(Discoverable); ok && d.Discoverable() {
			return nil
		}
	}
	return fmt.Errorf("model %q not served by provider %q — run 'maestro model list'", modelID, providerName)
}
