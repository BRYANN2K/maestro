package agentcore

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// maxRetryAttempts caps the total number of HTTP attempts (1 initial + 3
// retries) for a single provider request.
const maxRetryAttempts = 4

// retryMaxDelay caps any single wait between attempts.
const retryMaxDelay = 30 * time.Second

// retryableStatus reports whether the status code deserves a retry: rate
// limits, transient server errors, and Anthropic's overload code 529.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		529:                            // Anthropic overloaded
		return true
	}
	return false
}

// retryDelay returns the wait before the given attempt (1-based). Retry-After
// wins when present; otherwise exponential backoff 2s * 2^(attempt-1) with
// ±20% jitter, capped at retryMaxDelay.
func retryDelay(attempt int, retryAfter string) time.Duration {
	if retryAfter != "" {
		if secs, err := strconv.Atoi(retryAfter); err == nil && secs >= 0 {
			return capDelay(time.Duration(secs) * time.Second)
		}
		if t, err := http.ParseTime(retryAfter); err == nil {
			if d := time.Until(t); d > 0 {
				return capDelay(d)
			}
		}
	}
	base := 2 * time.Second
	d := base * (1 << (attempt - 1))
	jitter := time.Duration(rand.Int63n(int64(d) / 5))
	if attempt%2 == 0 {
		return capDelay(d + jitter)
	}
	return capDelay(d - jitter)
}

func capDelay(d time.Duration) time.Duration {
	if d > retryMaxDelay {
		return retryMaxDelay
	}
	return d
}

// doWithRetry executes fn (an HTTP request) up to maxRetryAttempts times,
// retrying transport errors and retryable status codes. Failed response
// bodies are drained and closed so connections are reusable. Cancellation is
// never retried: context.Canceled propagates immediately.
func doWithRetry(ctx context.Context, fn func() (*http.Response, error)) (*http.Response, error) {
	var resp *http.Response
	var err error
	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		resp, err = fn()
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return nil, err
			}
			if attempt == maxRetryAttempts || !sleepCtx(ctx, retryDelay(attempt, "")) {
				return nil, err
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil
		}
		code := resp.StatusCode
		if !retryableStatus(code) || attempt == maxRetryAttempts {
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if !sleepCtx(ctx, retryDelay(attempt, resp.Header.Get("Retry-After"))) {
			return nil, context.Canceled
		}
	}
	return resp, err
}

// doRequestWithRetry replays a request with a fresh body for every attempt.
// net/http consumes and may close Request.Body even when Do returns a
// transport error; reusing the same request would retain ContentLength while
// sending zero bytes on the next attempt.
func doRequestWithRetry(ctx context.Context, client *http.Client, req *http.Request) (*http.Response, error) {
	return doWithRetry(ctx, func() (*http.Response, error) {
		attempt := req.Clone(ctx)
		if req.Body != nil {
			if req.GetBody == nil {
				return nil, errors.New("retry request body is not replayable")
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			attempt.Body = body
		}
		return client.Do(attempt)
	})
}

// sleepCtx sleeps d honoring ctx cancellation; returns false when cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
