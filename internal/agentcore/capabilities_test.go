package agentcore

import "testing"

func TestOAuthRuntimeSupportedFailsClosed(t *testing.T) {
	for _, provider := range []string{"codex", "anthropic", "xai", "github-copilot", "antigravity", "unknown"} {
		if OAuthRuntimeSupported(provider) {
			t.Errorf("OAuthRuntimeSupported(%q) = true without a token-consuming runtime", provider)
		}
	}
}
