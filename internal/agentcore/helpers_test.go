package agentcore

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// httptestServer starts a server whose handler is only invoked on the first
// request; it sets the SSE content type.
func httptestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}
