package orchestrator

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"

	"github.com/bryann2k/maestro/internal/agentcore"
	"github.com/bryann2k/maestro/internal/config"
	"github.com/bryann2k/maestro/internal/runtimebridge"
	"github.com/bryann2k/maestro/internal/vault"
)

// ProviderInfo is the listing form of a provider (§10.5).
type ProviderInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Source      string `json:"source"` // maestrorc | env | local
	KeySet      bool   `json:"key_set"`
	RequiresKey bool   `json:"requires_key"`
	Models      int    `json:"models"`
}

// ProviderList returns the active providers with their source.
func (o *Orchestrator) ProviderList(ctx context.Context) []ProviderInfo {
	registry := o.registry
	if registry == nil {
		return nil
	}
	catalog := registry.Catalog()
	var out []ProviderInfo
	for _, name := range registry.Providers() {
		p, ok := registry.Provider(name)
		if !ok {
			// A concurrent credential reload may replace the snapshot between
			// the names and provider lookups. The next refresh will include the
			// newly published state; this listing must never dereference nil.
			continue
		}
		source := "env"
		for _, cp := range catalog {
			if cp.ID == name {
				if isLocalCatalog(cp) {
					source = "local"
				}
				break
			}
		}
		if o.cfg != nil {
			for _, cp := range o.cfg.Providers {
				if cp.Name == name {
					source = "maestrorc"
					break
				}
			}
		}
		keySet := false
		if o.keys != nil {
			_, keySet = o.keys.Key(name)
		}
		if !keySet && o.cfg != nil {
			for _, configured := range o.cfg.Providers {
				if configured.Name == name && configured.APIKey != "" {
					keySet = true
					break
				}
			}
		}
		if !keySet {
			if cp, ok := catalog[name]; ok {
				keySet = providerEnvPresent(cp)
			}
		}
		models := len(p.Models())
		requiresKey := !isLocalProviderName(name)
		if o.cfg != nil {
			for _, configured := range o.cfg.Providers {
				if configured.Name == name && isLocalProviderType(configured.Type) {
					requiresKey = false
					break
				}
			}
		}
		out = append(out, ProviderInfo{
			Name: name, Type: providerTypeOf(p), Source: source, KeySet: keySet,
			RequiresKey: requiresKey, Models: models,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ProviderInfo returns one provider's authentication metadata.
func (o *Orchestrator) ProviderInfo(ctx context.Context, name string) (ProviderInfo, bool) {
	for _, info := range o.ProviderList(ctx) {
		if info.Name == name {
			return info, true
		}
	}
	if catalog := o.catalog(); catalog != nil {
		if provider, ok := catalog[name]; ok {
			return ProviderInfo{
				Name: name, Type: "openai-compat", Source: "catalog",
				RequiresKey: !isLocalProviderName(name), Models: len(provider.Models),
			}, true
		}
	}
	return ProviderInfo{}, false
}

func isLocalCatalog(cp agentcore.CatalogProvider) bool {
	switch cp.ID {
	case "ollama", "llamacpp", "lmstudio", "litellm":
		return true
	}
	return false
}

func isLocalProviderName(name string) bool {
	switch strings.ToLower(name) {
	case "ollama", "llamacpp", "lmstudio", "litellm":
		return true
	default:
		return false
	}
}

func isLocalProviderType(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "ollama", "llamacpp", "lmstudio", "litellm":
		return true
	default:
		return false
	}
}

func providerEnvPresent(provider agentcore.CatalogProvider) bool {
	for _, env := range provider.Env {
		if os.Getenv(env) != "" {
			return true
		}
	}
	return false
}

func providerTypeOf(p agentcore.Provider) string { return p.Type() }

func (o *Orchestrator) catalog() map[string]agentcore.CatalogProvider {
	if o.registry == nil {
		return nil
	}
	return o.registry.Catalog()
}

// ModelInfo is the listing form of a model.
type ModelInfo struct {
	ID         string  `json:"id"`
	Provider   string  `json:"provider"`
	Name       string  `json:"name"`
	Context    int     `json:"context_window"`
	PriceIn    float64 `json:"price_input"`
	PriceOut   float64 `json:"price_output"`
	Reasoning  bool    `json:"reasoning"`
	Discovered bool    `json:"discovered,omitempty"`
}

// ModelList returns every known model across providers. Models absent
// from the catalog are marked as discovered.
func (o *Orchestrator) ModelList(ctx context.Context) []ModelInfo {
	registry := o.registry
	if registry == nil {
		return nil
	}
	var out []ModelInfo
	seen := map[string]bool{}
	catalog := registry.Catalog()
	for _, name := range registry.Providers() {
		p, ok := registry.Provider(name)
		if !ok {
			continue
		}
		cat := catalog[name]
		for _, m := range p.Models() {
			_, inCatalog := cat.Models[m.ID]
			seen[name+"\x00"+m.ID] = true
			out = append(out, ModelInfo{
				ID: m.ID, Provider: name, Name: m.Name,
				Context: m.ContextWindow, PriceIn: m.PriceInput, PriceOut: m.PriceOutput,
				Reasoning: m.CanReason, Discovered: !inCatalog && m.PriceInput == 0,
			})
		}
	}
	// Keep catalog-only providers visible so the picker is useful before a
	// provider key is configured. Runtime resolution still happens on send.
	for name, provider := range catalog {
		for id, model := range provider.Models {
			if model.Status == "deprecated" || seen[name+"\x00"+id] {
				continue
			}
			seen[name+"\x00"+id] = true
			out = append(out, ModelInfo{
				ID: id, Provider: name, Name: model.Name,
				Context: model.Limit.Context, PriceIn: model.Cost.Input,
				PriceOut: model.Cost.Output, Reasoning: model.Reasoning,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ProviderAdd appends a provider to the project or global maestrorc.
func (o *Orchestrator) ProviderAdd(ctx context.Context, p config.Provider, global bool) error {
	if err := config.ValidateProvider(p); err != nil {
		return fmt.Errorf("provider add: %w", err)
	}
	if p.Type == "openai-compat" {
		p.DiscoverModels = true
	}
	key := strings.TrimSpace(p.APIKey)
	p.APIKey = "" // credentials belong in the vault, never in maestrorc
	if key != "" {
		if o.vault == nil {
			return errors.New("provider add: vault unavailable; add the provider without --api-key, then run `maestro auth login <provider>`")
		}
		o.vault.Set("key:"+p.Name, key)
		if err := o.vault.Save(ctx); err != nil {
			return fmt.Errorf("provider add: store API key: %w", err)
		}
	}
	path := o.configPath(global)
	line, err := config.ProviderLine(p)
	if err != nil {
		return fmt.Errorf("provider add: %w", err)
	}
	if err := config.AppendProvider(path, line); err != nil {
		return err
	}
	// Publish a new config snapshot so the running harness can discover and
	// select this endpoint without restarting. In-flight catalog refreshes
	// retain their old snapshot and are invalidated by reloadRegistryLocked.
	o.registryMu.Lock()
	defer o.registryMu.Unlock()
	if o.cfg == nil || o.registry == nil {
		// Config-only embedders have no running provider registry.
		fmt.Fprintf(o.out, "provider %s added to %s\n", terminalSafeLine(p.Name), terminalSafeLine(path))
		return nil
	}
	next := *o.cfg
	next.Providers = append(append([]config.Provider(nil), o.cfg.Providers...), p)
	o.cfg = &next
	if err := o.reloadRegistryLocked(ctx); err != nil {
		return fmt.Errorf("provider saved, but activation failed: %w", err)
	}
	fmt.Fprintf(o.out, "provider %s added to %s\n", terminalSafeLine(p.Name), terminalSafeLine(path))
	return nil
}

// ProviderRemove removes a provider from the config file.
func (o *Orchestrator) ProviderRemove(ctx context.Context, name string, global bool) error {
	if err := config.ValidateProviderID(name); err != nil {
		return fmt.Errorf("provider remove: %w", err)
	}
	path := o.configPath(global)
	if err := config.RemoveProvider(path, name); err != nil {
		return err
	}
	fmt.Fprintf(o.out, "provider %s removed from %s\n", terminalSafeLine(name), terminalSafeLine(path))
	return nil
}

func (o *Orchestrator) configPath(global bool) string {
	if global {
		if p, err := config.GlobalPath(); err == nil {
			return p
		}
	}
	return filepath.Join(o.baseDir, "maestrorc")
}

// AuthLogin prompts for an API key and stores it in the vault.
func (o *Orchestrator) AuthLogin(ctx context.Context, provider string) error {
	if !o.providerKnown(provider) {
		return fmt.Errorf("auth: unknown provider %q — run `maestro provider list`", provider)
	}
	if o.vault == nil {
		return errors.New("auth: vault unavailable")
	}
	fmt.Fprintf(o.out, "API key for %s: ", terminalSafeLine(provider))
	var key string
	if input, ok := o.in.(interface{ Fd() uintptr }); ok && term.IsTerminal(int(input.Fd())) {
		secret, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(o.out)
		if err != nil {
			return fmt.Errorf("auth: read key: %w", err)
		}
		key = strings.TrimSpace(string(secret))
	} else {
		reader := bufio.NewReader(o.in)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return fmt.Errorf("auth: %w", err)
		}
		key = strings.TrimSpace(line)
	}
	if key == "" {
		return errors.New("auth: empty key")
	}
	o.vault.Set("key:"+provider, key)
	if err := o.vault.Save(ctx); err != nil {
		return err
	}
	if err := o.reloadRegistry(ctx); err != nil {
		return err
	}
	fmt.Fprintf(o.out, "key for %s stored (AES-256 vault)\n", terminalSafeLine(provider))
	return nil
}

// AuthAPIKey stores a provider API key and rebuilds the live registry so the
// newly authenticated model can be selected without restarting the TUI.
func (o *Orchestrator) AuthAPIKey(ctx context.Context, provider, key string) error {
	if !o.providerKnown(provider) {
		return fmt.Errorf("auth: unknown provider %q", provider)
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("auth: empty API key")
	}
	if o.vault == nil {
		return errors.New("auth: vault unavailable")
	}
	o.vault.Set("key:"+provider, strings.TrimSpace(key))
	if err := o.vault.Save(ctx); err != nil {
		return err
	}
	return o.reloadRegistry(ctx)
}

func (o *Orchestrator) reloadRegistry(ctx context.Context) error {
	o.registryMu.Lock()
	defer o.registryMu.Unlock()
	return o.reloadRegistryLocked(ctx)
}

func (o *Orchestrator) reloadRegistryLocked(ctx context.Context) error {
	if o.closed {
		return errors.New("auth: orchestrator is closed")
	}
	if o.cfg == nil {
		return errors.New("auth: provider configuration unavailable")
	}
	// A credential/config reload is a newer registry generation than any
	// catalog fetch already in flight. That older fetch may finish, but it must
	// not replace providers built with these newer credentials.
	o.modelRefreshGen++
	registry := o.registry
	if registry == nil {
		return errors.New("auth: provider registry unavailable")
	}
	catalog := registry.Catalog()
	keys := o.keys
	if keys == nil {
		keys = vaultKeyStore{vault: o.vault}
	}
	reg, err := agentcore.NewRegistry(ctx, o.cfg, keys, catalog)
	if err != nil && (reg == nil || len(reg.Providers()) == 0) {
		return fmt.Errorf("auth: reload provider: %w", err)
	}
	if reg != nil {
		if replaceErr := registry.ReplaceAndClose(reg); replaceErr != nil {
			return fmt.Errorf("auth: replace provider registry: %w", replaceErr)
		}
	}
	return nil
}

// vaultKeyStore bridges the vault's key:<provider> namespace to the
// agentcore KeyStore contract. The CLI normally supplies this adapter too;
// keeping it here makes the interactive auth flow work for embedders that
// only provide Options.Vault.
type vaultKeyStore struct{ vault *vault.Vault }

func (k vaultKeyStore) Key(name string) (string, bool) {
	if k.vault == nil {
		return "", false
	}
	return k.vault.Get("key:" + name)
}

// AuthStatus lists the active providers with their credential status.
func (o *Orchestrator) AuthStatus(ctx context.Context) {
	known := map[string]bool{}
	for _, name := range o.registry.Providers() {
		known[name] = true
	}
	for _, p := range o.cfg.Providers {
		known[p.Name] = true
	}
	if o.vault != nil {
		for _, key := range o.vault.Keys() {
			if rest, ok := strings.CutPrefix(key, "key:"); ok && rest != "" {
				known[rest] = true
			}
			if rest, ok := strings.CutPrefix(key, "oauth:"); ok {
				if name, _, found := strings.Cut(rest, ":"); found && name != "" {
					known[name] = true
				}
			}
		}
	}
	names := make([]string, 0, len(known))
	for n := range known {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(o.out, "%-20s %s\n", "PROVIDER", "STATUS")
	for _, name := range names {
		status := "—"
		if o.vault != nil {
			key, hasKey := o.vault.Get("key:" + name)
			_, hasOAuth := o.vault.Get("oauth:" + name + ":access")
			switch {
			case hasKey && agentcore.IsRuntimeCredential(key):
				status = "account"
			case hasKey:
				status = "api key"
			case hasOAuth:
				status = "oauth stored (runtime unsupported)"
			}
		}
		fmt.Fprintf(o.out, "%-20s %s\n", terminalSafeLine(name), terminalSafeLine(status))
	}
}

// AuthLogout removes the stored credentials for a provider.
func (o *Orchestrator) AuthLogout(ctx context.Context, provider string) error {
	if o.vault == nil {
		return errors.New("auth: vault unavailable")
	}
	o.vault.Delete("key:" + provider)
	o.vault.Delete("oauth:" + provider + ":access")
	o.vault.Delete("oauth:" + provider + ":refresh")
	if err := o.vault.Save(ctx); err != nil {
		return err
	}
	if o.cfg != nil {
		if err := o.reloadRegistry(ctx); err != nil {
			return err
		}
	}
	fmt.Fprintf(o.out, "credentials for %s removed\n", terminalSafeLine(provider))
	return nil
}

// AuthOAuth runs the provider's OAuth flow and stores the token.
func (o *Orchestrator) AuthOAuth(ctx context.Context, provider string) error {
	if !agentcore.OAuthRuntimeSupported(provider) {
		return fmt.Errorf("auth: no built-in account login for %q", provider)
	}
	if o.vault == nil {
		return errors.New("auth: vault unavailable")
	}
	reader := bufio.NewReader(o.in)
	stored := false
	err := runtimebridge.Run(ctx, map[string]any{"version": 1, "operation": "login", "provider": provider}, func(ctx context.Context, message runtimebridge.Message) (any, error) {
		switch message.Method {
		case "auth":
			var info struct {
				URL          string
				Instructions string
			}
			if err := json.Unmarshal(message.Params, &info); err != nil {
				return nil, err
			}
			fmt.Fprintf(o.out, "Open to connect %s:\n%s\n%s\n", terminalSafeLine(provider), terminalSafeLine(info.URL), terminalSafeLine(info.Instructions))
			return nil, nil
		case "prompt":
			var prompt struct {
				Message     string
				Placeholder string
			}
			if err := json.Unmarshal(message.Params, &prompt); err != nil {
				return nil, err
			}
			fmt.Fprintf(o.out, "%s: ", terminalSafeLine(prompt.Message))
			line, err := reader.ReadString('\n')
			return strings.TrimSpace(line), err
		case "credentials":
			var result struct{ Credentials json.RawMessage }
			if err := json.Unmarshal(message.Params, &result); err != nil {
				return nil, err
			}
			if len(result.Credentials) == 0 || string(result.Credentials) == "null" {
				return nil, errors.New("auth: missing credentials")
			}
			encoded, _ := json.Marshal(map[string]any{"maestro_oauth": result.Credentials})
			if err := (vaultKeyStore{vault: o.vault}).SaveKey(ctx, provider, string(encoded)); err != nil {
				return nil, err
			}
			stored = true
			return nil, nil
		default:
			return nil, fmt.Errorf("auth: unexpected runtime request %q", message.Method)
		}
	})
	if err != nil {
		return err
	}
	if !stored {
		return errors.New("auth: account login ended without a credential")
	}
	if err := o.reloadRegistry(ctx); err != nil {
		return err
	}
	fmt.Fprintf(o.out, "%s account connected to Maestro\n", terminalSafeLine(provider))
	return nil
}

// Vault exposes the vault (CLI wiring).
func (o *Orchestrator) Vault() *Vault { return o.vault }

// Vault is the vault type alias for command wiring.
type Vault = vault.Vault

func (o *Orchestrator) providerKnown(name string) bool {
	if o.registry != nil {
		for _, p := range o.registry.Providers() {
			if p == name {
				return true
			}
		}
	}
	if o.cfg != nil {
		for _, p := range o.cfg.Providers {
			if p.Name == name {
				return true
			}
		}
	}
	if _, ok := o.catalog()[name]; ok {
		return true
	}
	return false
}

// dispatchProvider / dispatchModel / dispatchAuth wire the CLI commands.
func (o *Orchestrator) dispatchProvider(ctx context.Context, cmd Command) error {
	sub := ""
	if len(cmd.Args) > 0 {
		sub = cmd.Args[0]
	}
	switch sub {
	case "list":
		if len(cmd.Args) != 1 {
			return errors.New("provider list: usage: maestro provider list")
		}
		if providerMutationFlagsSet(cmd) {
			return errors.New("provider list: add-only flags are not accepted")
		}
		for _, p := range o.ProviderList(ctx) {
			key := "no key"
			if p.KeySet {
				key = "key"
			}
			fmt.Fprintf(o.out, "  %-18s %-12s %-8s %-5s %d models\n",
				terminalSafeLine(p.Name), terminalSafeLine(p.Type), terminalSafeLine(p.Source), key, p.Models)
		}
		return nil
	case "add":
		if len(cmd.Args) != 2 {
			return errors.New("provider add: usage: maestro provider add <name> --type ... --base-url ... [--api-key]")
		}
		name := cmd.Args[1]
		global := flag(cmd, "global") == "true"
		p := config.Provider{
			Name: name, Type: flag(cmd, "type"), BaseURL: flag(cmd, "base-url"),
			APIKey: flag(cmd, "api-key"),
		}
		if p.Type == "" {
			p.Type = "openai-compat"
		}
		return o.ProviderAdd(ctx, p, global)
	case "remove":
		if len(cmd.Args) != 2 {
			return errors.New("provider remove: usage: maestro provider remove <name>")
		}
		if providerMutationFlagsSet(cmd) {
			return errors.New("provider remove: add-only flags are not accepted")
		}
		return o.ProviderRemove(ctx, cmd.Args[1], flag(cmd, "global") == "true")
	default:
		return errors.New("provider: usage: list | add <name> | remove <name>")
	}
}

func providerMutationFlagsSet(cmd Command) bool {
	return flag(cmd, "type") != "" || flag(cmd, "base-url") != "" || flag(cmd, "api-key") != ""
}

func (o *Orchestrator) dispatchModel(ctx context.Context, cmd Command) error {
	sub := ""
	if len(cmd.Args) > 0 {
		sub = cmd.Args[0]
	}
	if sub != "list" || len(cmd.Args) != 1 {
		return errors.New("model: usage: maestro model list [--json] [--provider X]")
	}
	filter := flag(cmd, "provider")
	models := o.ModelList(ctx)
	if filter != "" {
		filtered := make([]ModelInfo, 0, len(models))
		for _, model := range models {
			if model.Provider == filter {
				filtered = append(filtered, model)
			}
		}
		models = filtered
	}
	if flag(cmd, "json") == "true" {
		data, _ := json.MarshalIndent(models, "", "  ")
		fmt.Fprintln(o.out, string(terminalSafeJSON(data)))
		return nil
	}
	for _, m := range models {
		reasoning := ""
		if m.Reasoning {
			reasoning = " · reasoning"
		}
		fmt.Fprintf(o.out, "  %-30s %-18s ctx=%6d  $%.2f/$%.2f%s\n",
			terminalSafeLine(m.ID), terminalSafeLine(m.Provider), m.Context, m.PriceIn, m.PriceOut, reasoning)
	}
	return nil
}

func (o *Orchestrator) dispatchAuth(ctx context.Context, cmd Command) error {
	sub := ""
	if len(cmd.Args) > 0 {
		sub = cmd.Args[0]
	}
	switch sub {
	case "login":
		if len(cmd.Args) < 2 {
			return errors.New("auth login: usage: maestro auth login <provider>")
		}
		return o.AuthLogin(ctx, cmd.Args[1])
	case "status":
		o.AuthStatus(ctx)
		return nil
	case "logout":
		if len(cmd.Args) < 2 {
			return errors.New("auth logout: usage: maestro auth logout <provider>")
		}
		return o.AuthLogout(ctx, cmd.Args[1])
	case "oauth":
		if len(cmd.Args) < 2 {
			return fmt.Errorf("auth oauth: usage: maestro auth oauth <provider> (available: %v)", agentcore.AccountProviders())
		}
		return o.AuthOAuth(ctx, cmd.Args[1])
	default:
		return errors.New("auth: usage: login <provider> | status | logout <provider> | oauth <provider>")
	}
}

func (k vaultKeyStore) SaveKey(ctx context.Context, name, value string) error {
	if k.vault == nil {
		return fmt.Errorf("credential vault unavailable")
	}
	k.vault.Set("key:"+name, value)
	return k.vault.Save(ctx)
}

// ReloadCredentials applies a completed account login to the active registry.
func (o *Orchestrator) ReloadCredentials(ctx context.Context) error {
	if o.vault == nil {
		return errors.New("credential vault unavailable")
	}
	if err := o.vault.Reload(ctx); err != nil {
		return err
	}
	return o.reloadRegistry(ctx)
}
