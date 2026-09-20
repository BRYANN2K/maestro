//go:build unix

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSidebarLineCountRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact.pipe")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		if lines, ok := countFileNewlines(context.Background(), path, maxUntrackedLineCountBytes); ok || lines != 0 {
			t.Errorf("FIFO line count = %d, %v", lines, ok)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sidebar line count blocked opening a FIFO")
	}
}

func TestReviewFileCacheRejectsFIFOReplacementWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "source.go")
	if err := os.WriteFile(path, []byte("package source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache, err := newReviewFileCache(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	var hookErr error
	cache.afterLstat = func(string) {
		if err := os.Remove(path); err != nil {
			hookErr = err
			return
		}
		hookErr = syscall.Mkfifo(path, 0o600)
	}

	done := make(chan error, 1)
	go func() {
		_, err := cache.Read("source.go")
		done <- err
	}()
	select {
	case err := <-done:
		if hookErr != nil {
			t.Fatalf("replace file with FIFO: %v", hookErr)
		}
		if err == nil || !strings.Contains(err.Error(), "changed identity") {
			t.Fatalf("FIFO replacement error = %v, want changed-identity refusal", err)
		}
	case <-time.After(time.Second):
		t.Fatal("review source capture blocked opening a replacement FIFO")
	}
}

func TestGofmtPreflightRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "blocked.go")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	done := make(chan error, 1)
	go func() { done <- validateReviewRegularPath(root, "blocked.go") }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "not a regular source file") {
			t.Fatalf("gofmt FIFO preflight error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("gofmt preflight blocked opening a FIFO")
	}
}
