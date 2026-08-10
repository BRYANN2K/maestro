package config

import (
	"fmt"
	"strings"
)

// MaxProviderIDLength bounds identifiers that become config tokens, map keys,
// environment-variable prefixes, and the first segment of model handles.
const MaxProviderIDLength = 64

// ValidateProviderID enforces the one canonical provider identifier grammar:
// ASCII alphanumeric first, then ASCII alphanumeric, dot, underscore, or
// hyphen. It excludes whitespace, controls, path separators, and dot segments.
func ValidateProviderID(id string) error {
	if id == "" {
		return fmt.Errorf("provider name is required")
	}
	if len(id) > MaxProviderIDLength {
		return fmt.Errorf("provider name %q is too long (maximum %d ASCII characters)", id, MaxProviderIDLength)
	}
	for i := 0; i < len(id); i++ {
		ch := id[i]
		alphaNumeric := ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
		if alphaNumeric || i > 0 && (ch == '.' || ch == '_' || ch == '-') {
			continue
		}
		return fmt.Errorf("provider name %q is invalid: use an ASCII letter or digit first, followed only by letters, digits, '.', '_', or '-'", id)
	}
	return nil
}

// ValidateProvider applies the runtime provider contract before credentials
// or configuration are mutated. Anthropic has a built-in endpoint; OpenAI-
// compatible and local transports require an explicit base URL.
func ValidateProvider(provider Provider) error {
	if err := ValidateProviderID(provider.Name); err != nil {
		return err
	}
	switch provider.Type {
	case "anthropic":
		return nil
	case "openai", "openai-compat", "ollama", "llamacpp", "lmstudio", "litellm":
		if strings.TrimSpace(provider.BaseURL) == "" {
			return fmt.Errorf("provider %s: --base-url is required for type %s", provider.Name, provider.Type)
		}
		return nil
	case "anthropic-oauth", "openai-codex-oauth", "gemini-oauth", "perplexity-oauth":
		return fmt.Errorf("provider %s: OAuth provider type %s is not supported by the native runtime", provider.Name, provider.Type)
	case "bedrock", "vertexai":
		return fmt.Errorf("provider %s: type %s is not supported by the native runtime", provider.Name, provider.Type)
	case "":
		return fmt.Errorf("provider %s: --type is required", provider.Name)
	default:
		return fmt.Errorf("provider %s: unknown type %q", provider.Name, provider.Type)
	}
}
