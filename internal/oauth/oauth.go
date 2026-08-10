// Package oauth implements provider OAuth flows for Maestro (§10.6): the
// authorization-code flows used by codex/chatgpt, anthropic, and xAI
// (PKCE S256, local loopback callback), plus the device-code flows for
// GitHub Copilot and Google Antigravity/Gemini. Tokens are stored by the
// caller (the AES vault) and refreshed with the stored refresh token.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Flow describes one provider OAuth flow.
type Flow struct {
	Name          string
	Kind          string // "code" | "device"
	AuthorizeURL  string
	TokenURL      string
	ClientID      string
	ClientSecret  string // optional (antigravity)
	Scopes        string
	ExtraAuth     map[string]string // extra authorize query params
	RedirectPath  string            // default /callback
	DeviceCodeURL string            // device kind
	DevicePageURL string            // human verification page
}

// Token is the result of an OAuth exchange.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	AccountID    string
	Extra        map[string]string
}

// Flows is the provider registry (§10.6).
var Flows = map[string]Flow{
	"codex": {
		Name: "codex", Kind: "code",
		AuthorizeURL: "https://auth.openai.com/oauth/authorize",
		TokenURL:     "https://auth.openai.com/oauth/token",
		ClientID:     "app_EMoamEEZ73f0CkXaXp7hrann",
		Scopes:       "openid profile email offline_access",
		ExtraAuth: map[string]string{
			"id_token_add_organizations": "true",
			"codex_cli_simplified_flow":  "true",
			"originator":                 "maestro",
		},
	},
	"anthropic": {
		Name: "anthropic", Kind: "code",
		AuthorizeURL: "https://claude.ai/oauth/authorize",
		TokenURL:     "https://console.anthropic.com/v1/oauth/token",
		ClientID:     "9d1c250a-e61b-44d9-88ed-5944d1962f5e",
		Scopes:       "org:create_api_key user:profile user:inference",
	},
	"xai": {
		Name: "xai", Kind: "code",
		AuthorizeURL: "https://auth.x.ai/oauth2/authorize",
		TokenURL:     "https://auth.x.ai/oauth2/token",
		ClientID:     "b1a00492-073a-47ea-816f-4c329264a828",
		Scopes:       "openid profile email offline_access grok-cli:access api:access",
		ExtraAuth:    map[string]string{"plan": "generic", "referrer": "maestro"},
	},
	"github-copilot": {
		Name: "github-copilot", Kind: "device",
		ClientID:      "Ov23li8tweQw6odWQebz",
		Scopes:        "read:user",
		DeviceCodeURL: "https://github.com/login/device/code",
		TokenURL:      "https://github.com/login/oauth/access_token",
		DevicePageURL: "https://github.com/login/device",
	},
	"antigravity": {
		Name: "antigravity", Kind: "code",
		AuthorizeURL: "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		ClientID:     "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com",
		Scopes:       "https://www.googleapis.com/auth/cloud-platform https://www.googleapis.com/auth/userinfo.email https://www.googleapis.com/auth/userinfo.profile",
		RedirectPath: "/oauth2callback",
	},
}

var httpClient = &http.Client{Timeout: 30 * time.Second}

// Names returns the available flow names.
func Names() []string {
	out := make([]string, 0, len(Flows))
	for n := range Flows {
		out = append(out, n)
	}
	return out
}

// Authorize runs the flow end-to-end. open is called with the URL the user
// must visit (the caller prints or opens a browser).
func Authorize(ctx context.Context, f Flow, open func(url string)) (Token, error) {
	switch f.Kind {
	case "code":
		return authorizeCode(ctx, f, open)
	case "device":
		return authorizeDevice(ctx, f, open)
	default:
		return Token{}, fmt.Errorf("oauth: unknown flow kind %q", f.Kind)
	}
}

// pkce generates the S256 verifier + challenge.
func pkce() (verifier, challenge string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

func randomState() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("oauth: generate state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// authorizeCode runs the authorization-code + PKCE flow with a local
// loopback callback.
func authorizeCode(ctx context.Context, f Flow, open func(url string)) (Token, error) {
	verifier, challenge, err := pkce()
	if err != nil {
		return Token{}, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Token{}, err
	}
	redirect := "http://" + ln.Addr().String() + orDefault(f.RedirectPath, "/callback")
	state, err := randomState()
	if err != nil {
		ln.Close()
		return Token{}, err
	}

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", f.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("scope", f.Scopes)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	for k, v := range f.ExtraAuth {
		q.Set(k, v)
	}
	authURL := f.AuthorizeURL + "?" + q.Encode()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc(orDefault(f.RedirectPath, "/callback"), func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("state") != state {
			errCh <- errors.New("oauth: state mismatch")
			http.Error(w, "state mismatch", http.StatusBadRequest)
			return
		}
		codeCh <- r.URL.Query().Get("code")
		fmt.Fprint(w, "OK — you can close this tab.")
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	open(authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return Token{}, err
	case <-ctx.Done():
		return Token{}, ctx.Err()
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("client_id", f.ClientID)
	form.Set("code_verifier", verifier)
	if f.ClientSecret != "" {
		form.Set("client_secret", f.ClientSecret)
	}
	return exchange(ctx, f, form)
}

// authorizeDevice runs the RFC 8628 device flow.
func authorizeDevice(ctx context.Context, f Flow, open func(url string)) (Token, error) {
	form := url.Values{}
	form.Set("client_id", f.ClientID)
	if f.Scopes != "" {
		form.Set("scope", f.Scopes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.DeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("oauth: device code: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("oauth: device code: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("oauth: device code response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Token{}, fmt.Errorf("oauth: device code: HTTP %s: %s", resp.Status, truncate(string(body), 200))
	}
	var dev struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		Error           string `json:"error"`
		ErrorDesc       string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &dev); err != nil {
		return Token{}, fmt.Errorf("oauth: device code: %s", truncate(string(body), 200))
	}
	if dev.Error != "" {
		return Token{}, fmt.Errorf("oauth: device code: %s", dev.ErrorDesc)
	}
	page := dev.VerificationURI
	if page == "" {
		page = f.DevicePageURL
	}
	open(fmt.Sprintf("%s?user_code=%s", page, dev.UserCode))

	interval := dev.Interval
	if interval <= 0 {
		interval = 5
	}
	poll := url.Values{}
	poll.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	poll.Set("device_code", dev.DeviceCode)
	poll.Set("client_id", f.ClientID)
	// GitHub requires Accept: application/json.
	for {
		select {
		case <-ctx.Done():
			return Token{}, ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.TokenURL, strings.NewReader(poll.Encode()))
		if err != nil {
			return Token{}, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		r, err := httpClient.Do(req)
		if err != nil {
			return Token{}, err
		}
		data, readErr := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		r.Body.Close()
		if readErr != nil {
			return Token{}, fmt.Errorf("oauth: poll response: %w", readErr)
		}
		if r.StatusCode < 200 || r.StatusCode >= 300 {
			return Token{}, fmt.Errorf("oauth: poll: HTTP %s: %s", r.Status, truncate(string(data), 200))
		}
		var tok struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
			Error        string `json:"error"`
		}
		if err := json.Unmarshal(data, &tok); err != nil {
			return Token{}, fmt.Errorf("oauth: poll: %s", truncate(string(data), 200))
		}
		switch tok.Error {
		case "", "slow_down", "authorization_pending":
			if tok.Error == "slow_down" {
				interval += 5
			}
			if tok.AccessToken != "" {
				return Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, ExpiresIn: tok.ExpiresIn}, nil
			}
			continue
		case "access_denied", "expired_token":
			return Token{}, fmt.Errorf("oauth: %s", tok.Error)
		default:
			return Token{}, fmt.Errorf("oauth: %s", tok.Error)
		}
	}
}

// exchange performs the token exchange with the given form.
func exchange(ctx context.Context, f Flow, form url.Values) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Token{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Token{}, fmt.Errorf("oauth: exchange response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Token{}, fmt.Errorf("oauth: exchange: HTTP %s: %s", resp.Status, truncate(string(body), 200))
	}
	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return Token{}, fmt.Errorf("oauth: exchange: %s", truncate(string(body), 200))
	}
	if tok.Error != "" {
		return Token{}, fmt.Errorf("oauth: exchange: %s", tok.ErrorDesc)
	}
	if tok.AccessToken == "" {
		return Token{}, errors.New("oauth: empty access token")
	}
	return Token{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, ExpiresIn: tok.ExpiresIn}, nil
}

// Refresh refreshes an access token with the stored refresh token.
func Refresh(ctx context.Context, f Flow, refreshToken string) (Token, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", f.ClientID)
	if f.ClientSecret != "" {
		form.Set("client_secret", f.ClientSecret)
	}
	return exchange(ctx, f, form)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
