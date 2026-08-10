package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCodeFlowWithMockServer(t *testing.T) {
	// The token endpoint verifies the code + PKCE verifier.
	var gotGrant, gotCode, gotVerifier string
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotGrant = r.Form.Get("grant_type")
		gotCode = r.Form.Get("code")
		gotVerifier = r.Form.Get("code_verifier")
		if gotCode != "auth-code-123" {
			http.Error(w, "bad code", http.StatusBadRequest)
			return
		}
		if gotVerifier == "" {
			http.Error(w, "missing verifier", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"tok-abc","refresh_token":"ref-xyz","expires_in":3600}`)
	}))
	defer tokenSrv.Close()

	flow := Flow{
		Name: "test", Kind: "code",
		AuthorizeURL: "http://127.0.0.1:1/auth", // never reached: we inject the code via callback
		TokenURL:     tokenSrv.URL,
		ClientID:     "test-client",
		Scopes:       "read",
	}

	// Drive the flow: start Authorize in a goroutine, wait for the URL,
	// then hit the callback with the code.
	openedCh := make(chan string, 1)
	done := make(chan Token, 1)
	errCh := make(chan error, 1)
	go func() {
		tok, err := Authorize(context.Background(), flow, func(url string) {
			select {
			case openedCh <- url:
			default:
			}
		})
		if err != nil {
			errCh <- err
			return
		}
		done <- tok
	}()

	// Wait until the callback URL is known.
	var opened string
	select {
	case opened = <-openedCh:
	case <-time.After(5 * time.Second):
		t.Fatal("authorize URL never produced")
	}
	if !strings.Contains(opened, "code_challenge=") || !strings.Contains(opened, "code_challenge_method=S256") {
		t.Errorf("authorize URL missing PKCE: %s", opened)
	}
	// Parse the redirect base from the URL and hit it with a code.
	q := opened[strings.Index(opened, "?")+1:]
	redirect := ""
	for _, pair := range strings.Split(q, "&") {
		if strings.HasPrefix(pair, "redirect_uri=") {
			redirect = strings.ReplaceAll(pair[len("redirect_uri="):], "%3A", ":")
			redirect = strings.ReplaceAll(redirect, "%2F", "/")
		}
	}
	if redirect == "" {
		t.Fatal("no redirect_uri in authorize URL")
	}
	resp, err := http.Get(redirect + "?code=auth-code-123&state=" + stateOf(opened))
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	resp.Body.Close()

	select {
	case tok := <-done:
		if tok.AccessToken != "tok-abc" || tok.RefreshToken != "ref-xyz" {
			t.Errorf("token = %+v", tok)
		}
	case err := <-errCh:
		t.Fatalf("Authorize: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("Authorize never completed")
	}
	if gotGrant != "authorization_code" || gotVerifier == "" {
		t.Errorf("exchange: grant=%q verifier=%q", gotGrant, gotVerifier)
	}
}

func stateOf(authURL string) string {
	parts := strings.Split(authURL, "&")
	for _, p := range parts {
		if strings.HasPrefix(p, "state=") {
			return strings.TrimPrefix(p, "state=")
		}
	}
	return ""
}

func TestDeviceFlowWithMockServer(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/code"):
			fmt.Fprint(w, `{"device_code":"dev-1","user_code":"ABCD-EFGH","verification_uri":"http://127.0.0.1:1/verify","interval":1}`)
		default: // token poll
			polls++
			if polls < 2 {
				fmt.Fprint(w, `{"error":"authorization_pending"}`)
				return
			}
			fmt.Fprint(w, `{"access_token":"dev-tok","refresh_token":"dev-ref","expires_in":3600}`)
		}
	}))
	defer srv.Close()

	flow := Flow{
		Name: "test-device", Kind: "device",
		DeviceCodeURL: srv.URL + "/code",
		TokenURL:      srv.URL + "/token",
		ClientID:      "dev-client",
	}
	var opened string
	openedCh := make(chan string, 1)
	tok, err := Authorize(context.Background(), flow, func(u string) { openedCh <- u })
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	select {
	case opened = <-openedCh:
	default:
	}
	if !strings.Contains(opened, "ABCD-EFGH") {
		t.Errorf("opened URL missing user code: %s", opened)
	}
	if tok.AccessToken != "dev-tok" {
		t.Errorf("token = %+v", tok)
	}
	if polls < 2 {
		t.Errorf("polls = %d, want >= 2", polls)
	}
}

func TestDeviceFlowInitialRequestHonorsContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := Authorize(ctx, Flow{
		Name: "blocked", Kind: "device", DeviceCodeURL: srv.URL, TokenURL: srv.URL,
		ClientID: "client",
	}, func(string) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Authorize error = %v, want deadline exceeded", err)
	}
}

func TestRefresh(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "ref-1" {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"access_token":"new-tok"}`)
	}))
	defer srv.Close()
	tok, err := Refresh(context.Background(), Flow{TokenURL: srv.URL, ClientID: "c"}, "ref-1")
	if err != nil || tok.AccessToken != "new-tok" {
		t.Fatalf("Refresh = %+v, %v", tok, err)
	}
}

func TestNames(t *testing.T) {
	names := Names()
	for _, want := range []string{"codex", "anthropic", "xai", "github-copilot", "antigravity"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("missing flow %s", want)
		}
	}
}

func TestAuthorizeCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Authorize(ctx, Flow{Kind: "code", AuthorizeURL: "http://127.0.0.1:1/a", TokenURL: "http://127.0.0.1:1/t", ClientID: "c"}, func(string) {})
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("cancelled authorize should error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("authorize did not abort on cancel")
	}
}
