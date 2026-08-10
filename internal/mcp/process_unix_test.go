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
	ctx, cancel := contextWithShortDeadline()
	defer cancel()
	_, _ = client.CallTool(ctx, "echo", map[string]any{"message": "hello"})

	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("escaped helper child PID: %v", err)
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

func contextWithShortDeadline() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 200*time.Millisecond)
}
