//go:build unix

package cockpit

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCockpitPreviewRejectsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readFile(root, "pipe"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("preview blocked on FIFO")
	}
}
