package agentcore

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func (b *blockingReadCloser) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

func TestTimeoutReaderStallClosesUnderlyingRead(t *testing.T) {
	body := &blockingReadCloser{closed: make(chan struct{})}
	r := newTimeoutReader(body, 20*time.Millisecond)
	defer r.Close()
	started := time.Now()
	if _, err := r.Read(make([]byte, 1)); err != errStreamStalled {
		t.Fatalf("stalled read error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("stall watchdog took %s", elapsed)
	}
	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("stalled reader was not closed")
	}
	select {
	case <-r.workerDone:
	case <-time.After(time.Second):
		t.Fatal("timeout reader pump survived a stalled close")
	}
}

func TestTimeoutReaderStreamsAcrossCallerBufferSizes(t *testing.T) {
	want := strings.Repeat("0123456789", 10_000)
	r := newTimeoutReader(io.NopCloser(strings.NewReader(want)), time.Second)
	defer r.Close()
	var got bytes.Buffer
	buffer := make([]byte, 37)
	if _, err := io.CopyBuffer(&got, r, buffer); err != nil {
		t.Fatal(err)
	}
	if got.String() != want {
		t.Fatalf("streamed bytes = %d, want %d", got.Len(), len(want))
	}
}

func BenchmarkTimeoutReader(b *testing.B) {
	data := bytes.Repeat([]byte("stream-data"), 1<<15)
	buffer := make([]byte, 4<<10)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for range b.N {
		r := newTimeoutReader(io.NopCloser(bytes.NewReader(data)), time.Second)
		for {
			_, err := r.Read(buffer)
			if err == io.EOF {
				break
			}
			if err != nil {
				b.Fatal(err)
			}
		}
		_ = r.Close()
	}
}
