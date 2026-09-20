package agentcore

import "testing"

func TestOAuthRuntimeSupportedFailsClosed(t *testing.T) {
	for _, name := range AccountProviders() {
		if !OAuthRuntimeSupported(name) {
			t.Errorf("missing account adapter %s", name)
		}
	}
	for _, name := range []string{"codex", "unknown", "antigravity"} {
		if OAuthRuntimeSupported(name) {
			t.Errorf("unknown account %s", name)
		}
	}
}
