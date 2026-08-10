package agentcore

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// errStreamStalled is returned by the SSE idle watchdog when the provider
// leaves the connection open without sending any data for streamIdleTimeout.
var errStreamStalled = errors.New("provider stream stalled (no data received)")

// streamIdleTimeout is the maximum silence between SSE events before the
// stream is considered stalled. Each received byte resets the timer, so
// long-running reasoning streams are unaffected. Injectable for tests.
var streamIdleTimeout = 90 * time.Second

// timeoutReader wraps an io.ReadCloser so a read that receives no data for
// the timeout returns errStreamStalled. A stalled underlying Read leaks
// until Close is called, which the stream's defer does on error.
type timeoutReader struct {
	r       io.ReadCloser
	timeout time.Duration
}

func newTimeoutReader(r io.ReadCloser, timeout time.Duration) *timeoutReader {
	return &timeoutReader{r: r, timeout: timeout}
}

// Close closes the underlying body (unblocks a stalled Read).
func (t *timeoutReader) Close() error { return t.r.Close() }

// Read implements io.Reader with a sliding deadline.
func (t *timeoutReader) Read(p []byte) (int, error) {
	if t.timeout <= 0 {
		return t.r.Read(p)
	}
	type res struct {
		n   int
		err error
	}
	done := make(chan res, 1)
	go func() {
		n, err := t.r.Read(p)
		done <- res{n, err}
	}()
	select {
	case r := <-done:
		return r.n, r.err
	case <-time.After(t.timeout):
		return 0, errStreamStalled
	}
}

// providerTransport is the shared HTTP transport for provider clients:
// bounded dial, TLS handshake, and response-header waits so a silent API
// fails fast instead of hanging the run forever. There is deliberately no
// overall client timeout: long SSE streams must not be cut.
func providerTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
}
