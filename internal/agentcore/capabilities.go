package agentcore

import "strings"

// oauthRuntimeProviders is intentionally empty until a native provider can
// consume, refresh, and send the corresponding OAuth credential. A flow being
// available is not enough: advertising success before the runtime can use the
// token would leave users with a credential that can never authenticate a run.
var oauthRuntimeProviders = map[string]struct{}{}

// OAuthRuntimeSupported reports whether the native provider runtime can use
// OAuth credentials for provider. It fails closed for unknown providers.
func OAuthRuntimeSupported(provider string) bool {
	_, ok := oauthRuntimeProviders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}
