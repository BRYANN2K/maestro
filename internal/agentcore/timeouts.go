package agentcore

import (
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
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
// the timeout returns errStreamStalled. One persistent pump and one reusable
// timer serve the whole stream; the previous implementation allocated a
// goroutine, channel, and time.After timer for every SSE read.
type timeoutReader struct {
	r          io.ReadCloser
	timeout    time.Duration
	startOnce  sync.Once
	closeOnce  sync.Once
	readMu     sync.Mutex
	chunks     chan timeoutChunk
	ack        chan struct{}
	done       chan struct{}
	workerDone chan struct{}
	timer      *time.Timer
	pending    []byte
	pendingErr error
	terminal   error
	closeErr   error
}

type timeoutChunk struct {
	data []byte
	err  error
}

func newTimeoutReader(r io.ReadCloser, timeout time.Duration) *timeoutReader {
	t := &timeoutReader{r: r, timeout: timeout}
	if timeout > 0 {
		t.chunks = make(chan timeoutChunk)
		t.ack = make(chan struct{})
		t.done = make(chan struct{})
		t.workerDone = make(chan struct{})
		t.timer = time.NewTimer(time.Hour)
		if !t.timer.Stop() {
			<-t.timer.C
		}
	}
	return t
}

// Close closes the underlying body (unblocks a stalled Read).
func (t *timeoutReader) Close() error {
	t.closeOnce.Do(func() {
		if t.done != nil {
			close(t.done)
		}
		t.closeErr = t.r.Close()
	})
	return t.closeErr
}

// Read implements io.Reader with a sliding deadline.
func (t *timeoutReader) Read(p []byte) (int, error) {
	if t.timeout <= 0 {
		return t.r.Read(p)
	}
	if len(p) == 0 {
		return 0, nil
	}
	t.readMu.Lock()
	defer t.readMu.Unlock()
	if t.terminal != nil {
		return 0, t.terminal
	}
	t.startOnce.Do(func() { go t.pump() })
	if len(t.pending) > 0 {
		return t.consume(p)
	}

	for {
		t.resetTimer()
		select {
		case chunk := <-t.chunks:
			t.stopTimer()
			t.pending = chunk.data
			t.pendingErr = chunk.err
			if len(t.pending) == 0 {
				t.acknowledge()
				if chunk.err != nil {
					t.terminal = chunk.err
					return 0, chunk.err
				}
				continue
			}
			return t.consume(p)
		case <-t.timer.C:
			_ = t.Close()
			t.terminal = errStreamStalled
			return 0, errStreamStalled
		case <-t.done:
			t.stopTimer()
			t.terminal = io.ErrClosedPipe
			return 0, t.terminal
		}
	}
}

func (t *timeoutReader) pump() {
	defer close(t.workerDone)
	buffer := make([]byte, 32<<10)
	for {
		n, err := t.r.Read(buffer)
		select {
		case t.chunks <- timeoutChunk{data: buffer[:n], err: err}:
		case <-t.done:
			return
		}
		select {
		case <-t.ack:
		case <-t.done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (t *timeoutReader) consume(p []byte) (int, error) {
	n := copy(p, t.pending)
	t.pending = t.pending[n:]
	if len(t.pending) > 0 {
		return n, nil
	}
	err := t.pendingErr
	t.pendingErr = nil
	t.acknowledge()
	if err != nil {
		t.terminal = err
	}
	return n, err
}

func (t *timeoutReader) acknowledge() {
	select {
	case t.ack <- struct{}{}:
	case <-t.done:
	}
}

func (t *timeoutReader) resetTimer() {
	t.stopTimer()
	t.timer.Reset(t.timeout)
}

func (t *timeoutReader) stopTimer() {
	if !t.timer.Stop() {
		select {
		case <-t.timer.C:
		default:
		}
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
