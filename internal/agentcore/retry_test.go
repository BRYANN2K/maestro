package agentcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRetryableStatus(t *testing.T) {
	cases := []struct {
		code int
		want bool
	}{
		{429, true}, {500, true}, {502, true}, {503, true}, {529, true},
		{200, false}, {400, false}, {401, false}, {404, false}, {408, false},
	}
	for _, c := range cases {
		if got := retryableStatus(c.code); got != c.want {
			t.Errorf("retryableStatus(%d) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestRetryDelay(t *testing.T) {
	for attempt := 1; attempt <= 4; attempt++ {
		d := retryDelay(attempt, "")
		base := 2 * time.Second * (1 << (attempt - 1))
		lo, hi := base*4/5, base*6/5
		if attempt >= 5 {
			lo, hi = retryMaxDelay, retryMaxDelay
		}
		if d < lo || d > hi {
			t.Fatalf("attempt %d: delay %v outside [%v, %v]", attempt, d, lo, hi)
		}
	}
	if got := retryDelay(3, "5"); got != 5*time.Second {
		t.Errorf("Retry-After seconds = %v", got)
	}
	if got := retryDelay(3, "120"); got != retryMaxDelay {
		t.Errorf("Retry-After cap = %v", got)
	}
	if got := retryDelay(3, http.TimeFormat); got > retryMaxDelay || got < 0 {
		t.Errorf("Retry-After date = %v", got)
	}
	if got := retryDelay(3, "garbage"); got <= 0 || got > retryMaxDelay {
		t.Errorf("garbage Retry-After = %v", got)
	}
}

func TestDoWithRetrySuccessAfterFailures(t *testing.T) {
	var mu sync.Mutex
	var attempts []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		attempts = append(attempts, len(body))
		n := len(attempts)
		mu.Unlock()
		if n <= 2 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doRequestWithRetry(context.Background(), http.DefaultClient, req)
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "ok" {
		t.Errorf("body = %q", data)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 3 {
		t.Fatalf("attempts = %d (%v), want 3", len(attempts), attempts)
	}
	for i, n := range attempts {
		if n != 7 {
			t.Errorf("attempt %d body len = %d, want 7 (replay failed)", i, n)
		}
	}
}

func TestDoWithRetryGivesUp(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(529)
		fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("x"))
	resp, err := doWithRetry(context.Background(), func() (*http.Response, error) {
		return http.DefaultClient.Do(req.Clone(context.Background()))
	})
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 529 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if count != maxRetryAttempts {
		t.Errorf("attempts = %d, want %d", count, maxRetryAttempts)
	}
}

func TestDoWithRetryNonRetryable(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader("x"))
	resp, err := doWithRetry(context.Background(), func() (*http.Response, error) {
		return http.DefaultClient.Do(req.Clone(context.Background()))
	})
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || count != 1 {
		t.Errorf("status = %d, attempts = %d", resp.StatusCode, count)
	}
}

func TestDoWithRetryCancelled(t *testing.T) {
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL, strings.NewReader("x"))
	_, err := doWithRetry(ctx, func() (*http.Response, error) {
		if count == 1 {
			cancel()
		}
		return http.DefaultClient.Do(req.Clone(ctx))
	})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
