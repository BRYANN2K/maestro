//go:build unix

package tools

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReadRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stream.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := NewRead().Run(context.Background(), map[string]any{"path": path})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular file") {
			t.Fatalf("FIFO read error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("read blocked opening a FIFO")
	}
}
