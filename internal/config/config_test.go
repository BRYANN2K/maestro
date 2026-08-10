package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fullRc = `
# Providers
provider add deepseek --type openai-compat \
  --base-url "https://api.deepseek.com/v1" --api-key "$DEEPSEEK_API_KEY" \
  --discover-models
provider add ollama --type ollama --base-url "http://localhost:11434/v1" \
  --flat-rate --disable

# Models + pricing
model add deepseek/deepseek-chat --name "Deepseek V3" --context-window 64000 \
  --price-input 0.27 --price-output 1.1 --price-cache-create 1.1 --price-cache-hit 0.07 \
  --can-reason --supports-images --reasoning-effort high

# Slots
model large openai/gpt-4o --think --reasoning-effort high --temperature 0.2
model small anthropic/claude-haiku-4 --max-tokens 4096 --top-p 0.9 --top-k 50

# Roles
modelRoles:
  default: deepseek/deepseek-chat
  slow:    anthropic/claude-sonnet-4 --think --reasoning-effort high
  smol:    openai/gpt-4o-mini --max-tokens 4096

# MCP
mcp add github --type http --url "https://api.githubcopilot.com/mcp/" \
  --header Authorization "Bearer $(op read 'op://secret')"

# Permissions
permissions allow view edit grep
permissions deny bash

# LSP
lsp add go --command "gopls"

# Options
option auto-summarize true
option provider-auto-update true
`

func TestParseFull(t *testing.T) {
	cfg, err := Parse("maestrorc", fullRc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if len(cfg.Providers) != 2 {
		t.Fatalf("providers = %d, want 2", len(cfg.Providers))
	}
	ds := cfg.Providers[0]
	if ds.Name != "deepseek" || ds.Type != "openai-compat" || ds.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("deepseek = %+v", ds)
	}
	if !ds.DiscoverModels || ds.APIKey != "$DEEPSEEK_API_KEY" {
		t.Errorf("deepseek flags = %+v", ds)
	}
	if !cfg.Providers[1].FlatRate || !cfg.Providers[1].Disabled {
		t.Errorf("ollama = %+v", cfg.Providers[1])
	}

	if len(cfg.Models) != 1 {
		t.Fatalf("models = %d, want 1", len(cfg.Models))
	}
	m := cfg.Models[0]
	if m.ID != "deepseek/deepseek-chat" || m.ContextWindow != 64000 || m.PriceInput != 0.27 || m.PriceCacheHit != 0.07 {
		t.Errorf("model = %+v", m)
	}
	if !m.CanReason || !m.SupportsImages || m.ReasoningEffort != "high" {
		t.Errorf("model flags = %+v", m)
	}

	large, ok := cfg.Slots["large"]
	if !ok || large.Model != "openai/gpt-4o" || !large.Sampling.Think || large.Sampling.ReasoningEffort != "high" {
		t.Errorf("slot large = %+v", large)
	}
	if large.Sampling.Temperature == nil || *large.Sampling.Temperature != 0.2 {
		t.Errorf("large temperature = %v", large.Sampling.Temperature)
	}
	small := cfg.Slots["small"]
	if small.Sampling.MaxTokens != 4096 || small.Sampling.TopP == nil || *small.Sampling.TopP != 0.9 || small.Sampling.TopK != 50 {
		t.Errorf("slot small = %+v", small)
	}

	slow, ok := cfg.ModelRoles["slow"]
	if !ok || slow.Model != "anthropic/claude-sonnet-4" || !slow.Sampling.Think || slow.Sampling.ReasoningEffort != "high" {
		t.Errorf("role slow = %+v", slow)
	}
	if cfg.ModelRoles["default"].Model != "deepseek/deepseek-chat" {
		t.Errorf("role default = %+v", cfg.ModelRoles["default"])
	}
	if cfg.ModelRoles["smol"].Sampling.MaxTokens != 4096 {
		t.Errorf("role smol = %+v", cfg.ModelRoles["smol"])
	}

	if len(cfg.Mcp) != 1 {
		t.Fatalf("mcp = %d, want 1", len(cfg.Mcp))
	}
	gh := cfg.Mcp[0]
	if gh.Type != "http" || gh.URL != "https://api.githubcopilot.com/mcp/" || len(gh.Headers) != 1 {
		t.Errorf("mcp = %+v", gh)
	}
	if gh.Headers[0] != "Authorization Bearer $(op read 'op://secret')" {
		t.Errorf("mcp header = %q", gh.Headers[0])
	}

	if len(cfg.Permissions) != 2 || cfg.Permissions[0].Action != "allow" || len(cfg.Permissions[0].Tools) != 3 {
		t.Errorf("permissions = %+v", cfg.Permissions)
	}
	if cfg.Permissions[1].Action != "deny" || cfg.Permissions[1].Tools[0] != "bash" {
		t.Errorf("permissions deny = %+v", cfg.Permissions[1])
	}

	if len(cfg.LSP) != 1 || cfg.LSP[0].Name != "go" || cfg.LSP[0].Command != "gopls" {
		t.Errorf("lsp = %+v", cfg.LSP)
	}
	if cfg.Options["auto-summarize"] != "true" || cfg.Options["provider-auto-update"] != "true" {
		t.Errorf("options = %+v", cfg.Options)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{"unknown command", "frobnicate x\n", `unknown command "frobnicate"`},
		{"provider missing flags", "provider add x --type\n", "requires a value"},
		{"bad int", "model add x --context-window notanumber\n", "Atoi"},
		{"bad float", "model add x --price-input abc\n", "ParseFloat"},
		{"unknown flag", "provider add x --bogus v\n", "unknown provider flag"},
		{"unterminated quote", "option a \"unclosed\n", "unterminated quote"},
		{"bad permissions", "permissions explode\n", "invalid"},
		{"role without model", "modelRoles:\n  default:\n", "model is required"},
		{"positional after flag", "provider add x value --type openai\n", "unexpected positional"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("t.rc", tt.src)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse(%q) = %v, want containing %q", tt.name, err, tt.want)
			}
		})
	}
}

func TestParseMCPTransportContracts(t *testing.T) {
	t.Run("stdio command", func(t *testing.T) {
		cfg, err := Parse("t.rc", `mcp add local --type stdio --command "node server.js --safe"`)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Mcp) != 1 || cfg.Mcp[0].Command != "node server.js --safe" || cfg.Mcp[0].URL != "" {
			t.Fatalf("mcp = %+v", cfg.Mcp)
		}
	})

	for _, tc := range []struct {
		name string
		src  string
		want string
	}{
		{"stdio missing command", `mcp add local --type stdio`, "requires --command"},
		{"missing server name", `mcp add --type stdio --command tool`, "server name is required"},
		{"stdio ambiguous url", `mcp add local --type stdio --command tool --url https://example.test/mcp`, "does not accept --url"},
		{"http missing url", `mcp add remote --type http`, "requires --url"},
		{"http ambiguous command", `mcp add remote --type http --url https://example.test/mcp --command tool`, "does not accept --command"},
		{"unknown transport", `mcp add x --type websocket --url https://example.test/mcp`, "unsupported mcp type"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse("t.rc", tc.src)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestParseLineNumbers(t *testing.T) {
	_, err := Parse("t.rc", "provider add ok --type openai\n\nfrobnicate x\n")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "t.rc:3") {
		t.Errorf("error should carry file:line, got %v", err)
	}
}

func TestLoadPriority(t *testing.T) {
	project := t.TempDir()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)

	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write(filepath.Join(home, "maestro", "maestrorc"), "option global true\noption who global\n")
	write(filepath.Join(project, "maestrorc"), "option project true\noption who project\n")
	write(filepath.Join(project, ".maestrorc"), "option who hidden\n")

	cfg, err := Load(context.Background(), project)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Options["global"] != "true" || cfg.Options["project"] != "true" {
		t.Errorf("options missing: %+v", cfg.Options)
	}
	if cfg.Options["who"] != "hidden" {
		t.Errorf("option who = %q, want hidden (highest priority)", cfg.Options["who"])
	}
}

func TestLoadMissingFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := Load(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("providers = %+v, want none", cfg.Providers)
	}
}

func TestLoadAppendsListsOverridesMaps(t *testing.T) {
	project := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	write := func(p, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	write(filepath.Join(project, "maestrorc"), "provider add p1 --type openai\noption color red\n")
	write(filepath.Join(project, ".maestrorc"), "provider add p2 --type anthropic\noption color blue\n")

	cfg, err := Load(context.Background(), project)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Providers) != 2 {
		t.Errorf("providers = %d, want 2 (appended)", len(cfg.Providers))
	}
	if cfg.Options["color"] != "blue" {
		t.Errorf("color = %q, want blue (overridden)", cfg.Options["color"])
	}
}

func TestProviderLineNeverSerializesAPIKey(t *testing.T) {
	line, err := ProviderLine(Provider{
		Name: "private", Type: "openai-compat", BaseURL: "https://example.test/v1", APIKey: "sk-secret",
	})
	if err != nil {
		t.Fatalf("ProviderLine: %v", err)
	}
	if strings.Contains(line, "sk-secret") || strings.Contains(line, "--api-key") {
		t.Fatalf("ProviderLine leaked an API key: %q", line)
	}
}

func TestProviderIdentifierValidationAndRoundTrip(t *testing.T) {
	const valid = "acme_1.prod-west"
	provider := Provider{Name: valid, Type: "openai-compat", BaseURL: "https://acme.test/v1"}
	line, err := ProviderLine(provider)
	if err != nil {
		t.Fatalf("ProviderLine valid ID: %v", err)
	}
	cfg, err := Parse("roundtrip.rc", line+"\n")
	if err != nil {
		t.Fatalf("Parse roundtrip: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].Name != valid {
		t.Fatalf("provider roundtrip = %+v", cfg.Providers)
	}

	for _, invalid := range []string{
		"has space", "line\noption injected true", "has/slash", "-leading", ".", "..",
		"unicodé", strings.Repeat("a", MaxProviderIDLength+1),
	} {
		t.Run(invalid, func(t *testing.T) {
			if err := ValidateProviderID(invalid); err == nil {
				t.Fatalf("ValidateProviderID(%q) succeeded", invalid)
			}
			if _, err := ProviderLine(Provider{Name: invalid, Type: "openai-compat", BaseURL: "https://invalid.test/v1"}); err == nil {
				t.Fatalf("ProviderLine(%q) succeeded", invalid)
			}
		})
	}

	for _, source := range []string{
		`provider add "has space" --type openai-compat --base-url https://invalid.test/v1`,
		`provider add has/slash --type openai-compat --base-url https://invalid.test/v1`,
		"provider add " + strings.Repeat("a", MaxProviderIDLength+1) + " --type openai-compat --base-url https://invalid.test/v1",
	} {
		if _, err := Parse("invalid.rc", source+"\n"); err == nil || !strings.Contains(err.Error(), "provider name") {
			t.Fatalf("Parse(%q) = %v, want provider-name error", source, err)
		}
	}
}

func TestValidateProviderRuntimeContract(t *testing.T) {
	for _, provider := range []Provider{
		{Name: "custom", Type: "openai-compat"},
		{Name: "custom", Type: "unknown", BaseURL: "https://example.test/v1"},
	} {
		if err := ValidateProvider(provider); err == nil {
			t.Fatalf("ValidateProvider(%+v) succeeded", provider)
		}
	}
	if err := ValidateProvider(Provider{Name: "anthropic-custom", Type: "anthropic"}); err != nil {
		t.Fatalf("Anthropic built-in endpoint rejected: %v", err)
	}
}

func TestCommentsAndContinuation(t *testing.T) {
	t.Run("continuation joins", func(t *testing.T) {
		src := "option a \\\n  true\noption b \"three # not a comment\"\n"
		cfg, err := Parse("t.rc", src)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if cfg.Options["a"] != "true" {
			t.Errorf("a = %q, want %q", cfg.Options["a"], "true")
		}
		if cfg.Options["b"] != "three # not a comment" {
			t.Errorf("b = %q", cfg.Options["b"])
		}
	})
	t.Run("comment inside continuation ends the line, bash-style", func(t *testing.T) {
		src := "option a one \\\n  # comment\n  two\n"
		cfg, err := Parse("t.rc", src)
		if err == nil {
			// bash would error on "two" as a separate command
			t.Fatalf("Parse should fail on orphan token, got %+v", cfg.Options)
		}
	})
}

func TestGlobalPath(t *testing.T) {
	p, err := GlobalPath()
	if err != nil {
		t.Fatalf("GlobalPath: %v", err)
	}
	if !strings.HasSuffix(p, string(os.PathSeparator)+"maestro"+string(os.PathSeparator)+"maestrorc") {
		t.Errorf("GlobalPath = %q, want .../maestro/maestrorc", p)
	}
}
