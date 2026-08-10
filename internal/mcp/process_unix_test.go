//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package mcp

import (
	"context"
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestEscapedMCPProcessHelper is re-executed as a malicious MCP descendant
// that deliberately leaves its parent's process group.
func TestEscapedMCPProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_ESCAPED_MCP_HELPER") != "1" {
		return
	}
	if err := syscall.Setpgid(0, 0); err != nil {
		t.Fatalf("Setpgid: %v", err)
	}
	if err := os.WriteFile(os.Getenv("MCP_CHILD_PID_FILE"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write child PID: %v", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestStdioCancellationKillsEscapedProcessGroupDescendant(t *testing.T) {
	pidFile := t.TempDir() + "/escaped-child.pid"
	t.Setenv("MCP_CHILD_PID_FILE", pidFile)
	client := helperClient(t, "escapedtreehang")
	if _, err := client.ListTools(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Starting a race-instrumented helper can take noticeably longer when the
	// full package suite is running in parallel. Keep the deadline bounded but
	// long enough to ensure the escaped descendant is actually created before
	// cancellation is exercised.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = client.CallTool(ctx, "echo", map[string]any{"message": "hello"})

	var raw []byte
	pidDeadline := time.Now().Add(2 * time.Second)
	for {
		var err error
		raw, err = os.ReadFile(pidFile)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || time.Now().After(pidDeadline) {
			t.Fatalf("escaped helper child PID: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil || pid <= 1 {
		t.Fatalf("child PID = %q, %v", raw, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("escaped MCP descendant %d survived cancellation", pid)
}
