package agentcore

import "slices"

// Account inference and refresh use the provider adapters bundled with Maestro.
func OAuthRuntimeSupported(provider string) bool {
	return slices.Contains(AccountProviders(), provider)
}
