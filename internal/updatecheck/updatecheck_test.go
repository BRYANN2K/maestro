package updatecheck

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCheckFetchesCachesAndComparesLatest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("User-Agent"); got != "maestro-update-check" {
			t.Errorf("User-Agent = %q", got)
		}
		fmt.Fprint(w, `{"version":"1.2.0"}`)
	}))
	defer server.Close()

	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cache := filepath.Join(t.TempDir(), "cache", "update.json")
	checker, err := New(Options{
		CurrentVersion: "v1.0.0", Endpoint: server.URL, CachePath: cache,
		Now: func() time.Time { return now }, Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := checker.Check(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Available || result.Latest != "1.2.0" || result.Current != "1.0.0" || result.FromCache {
		t.Fatalf("first result = %+v", result)
	}
	result, err = checker.Check(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.FromCache || requests.Load() != 1 {
		t.Fatalf("cached result = %+v, requests=%d", result, requests.Load())
	}
	if info, err := os.Stat(cache); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("cache info = %v, err=%v", info, err)
	}
}

func TestCheckForceBypassesCacheAndDoesNotOfferDowngrade(t *testing.T) {
	var version atomic.Value
	version.Store("1.0.0")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"version":%q}`, version.Load().(string))
	}))
	defer server.Close()
	checker, err := New(Options{CurrentVersion: "2.0.0", Endpoint: server.URL, CachePath: filepath.Join(t.TempDir(), "update.json"), Client: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	first, err := checker.Check(t.Context(), false)
	if err != nil || first.Available {
		t.Fatalf("first = %+v, err=%v", first, err)
	}
	version.Store("2.1.0")
	forced, err := checker.Check(t.Context(), true)
	if err != nil || !forced.Available || forced.Latest != "2.1.0" || forced.FromCache {
		t.Fatalf("forced = %+v, err=%v", forced, err)
	}
}

func TestAutomaticFailureIsCachedAndManualRetryStillRuns(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	checker, err := New(Options{
		CurrentVersion: "1.0.0", Endpoint: server.URL,
		CachePath: filepath.Join(t.TempDir(), "update.json"), Client: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := checker.Check(t.Context(), false); err == nil {
		t.Fatal("first automatic failure returned nil")
	}
	if _, err := checker.Check(t.Context(), false); err == nil || !strings.Contains(err.Error(), "recent registry check failed") {
		t.Fatalf("cached automatic failure = %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("automatic failures made %d requests, want 1", requests.Load())
	}
	if _, err := checker.Check(t.Context(), true); err == nil {
		t.Fatal("forced retry unexpectedly succeeded")
	}
	if requests.Load() != 2 {
		t.Fatalf("forced retry requests = %d, want 2", requests.Load())
	}
}

func TestCheckFailsClosedOnUntrustedResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		code int
		want string
	}{
		{name: "status", code: http.StatusUnauthorized, body: "secret body", want: "HTTP 401"},
		{name: "invalid json", code: http.StatusOK, body: "not json", want: "invalid JSON"},
		{name: "control version", code: http.StatusOK, body: `{"version":"1.1.0\u001b]52;c;x"}`, want: "registry version"},
		{name: "oversized", code: http.StatusOK, body: strings.Repeat("x", maxResponse+1), want: "exceeded"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.code)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			checker, err := New(Options{CurrentVersion: "1.0.0", Endpoint: server.URL, Client: server.Client()})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := checker.Check(t.Context(), true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Check error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCheckHonorsCancellationAndRejectsCacheSymlink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("fresh symlink cache should not be trusted as a network-free result")
	}))
	server.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(`{"checked_at":"2099-01-01T00:00:00Z","latest":"99.0.0"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "cache")
	if err := os.Symlink(target, cache); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	checker, err := New(Options{CurrentVersion: "1.0.0", Endpoint: server.URL, CachePath: cache, Client: server.Client(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := checker.Check(ctx, false); err == nil {
		t.Fatal("Check succeeded with cancelled network fallback")
	}
}

func TestSemanticVersionPrecedence(t *testing.T) {
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0",
	}
	for i := 0; i < len(ordered)-1; i++ {
		left, err := parseVersion(ordered[i])
		if err != nil {
			t.Fatal(err)
		}
		right, err := parseVersion(ordered[i+1])
		if err != nil {
			t.Fatal(err)
		}
		if left.compare(right) >= 0 {
			t.Fatalf("%s must precede %s", ordered[i], ordered[i+1])
		}
	}
	for _, invalid := range []string{"dev", "1", "1.0", "01.0.0", "1.0.0-01", "1.0.0 bad", "1.0.0\x1b[31m", "1.0.0+bad\x1b", "1.0.0+"} {
		if _, err := parseVersion(invalid); err == nil {
			t.Fatalf("parseVersion(%q) succeeded", invalid)
		}
	}
}
