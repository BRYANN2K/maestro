//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agent

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bryann2k/maestro/internal/agentcore"
)

const forkedLegacyScript = `
sleep 60 &
child=$!
printf '%s\n%s\n' "$$" "$child" > "$MAESTRO_PROCESS_TREE_PIDS"
wait "$child"
printf '{"type":"agent_message","text":"OUTPUT AFTER WAIT"}\n'
`

const escapedLegacyScript = `
MAESTRO_ESCAPED_PROCESS_HELPER=1 "$MAESTRO_TEST_BINARY" -test.run '^TestEscapedProcessHelper$' &
child=$!
while [ ! -s "$MAESTRO_ESCAPED_PROCESS_READY" ]; do
  sleep 0.01
done
printf '%s\n%s\n' "$$" "$child" > "$MAESTRO_PROCESS_TREE_PIDS"
wait "$child"
printf '{"type":"agent_message","text":"OUTPUT AFTER ESCAPED WAIT"}\n'
`

// TestEscapedProcessHelper re-executes the package test binary as a tool that
// deliberately leaves its vendor parent's process group. It is selected only
// by escapedLegacyScript; the regular package test invocation returns at once.
func TestEscapedProcessHelper(t *testing.T) {
	if os.Getenv("MAESTRO_ESCAPED_PROCESS_HELPER") != "1" {
		return
	}
	if err := syscall.Setpgid(0, 0); err != nil {
		t.Fatalf("Setpgid: %v", err)
	}
	if err := os.WriteFile(os.Getenv("MAESTRO_ESCAPED_PROCESS_READY"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatalf("write ready file: %v", err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestStreamingCancellationKillsLegacyProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/pids"
	t.Setenv("MAESTRO_PROCESS_TREE_PIDS", pidFile)
	t.Setenv("PATH", fakeBin(t, "codex", forkedLegacyScript))

	ctx, cancel := context.WithCancel(t.Context())
	ch, err := NewCodexAgent().Execute(ctx, "wait", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	pids := waitForProcessTree(t, pidFile)
	t.Cleanup(func() { killTestProcessTree(pids) })

	started := time.Now()
	cancel()
	events := collectEventsBounded(t, ch, 3*time.Second)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s, want <= 3s", elapsed)
	}
	if len(events) != 0 {
		t.Fatalf("cancelled stream emitted stale events: %+v", events)
	}
	assertProcessesGone(t, pids, 2*time.Second)
}

func TestCodexBlockedLargeStdinCancellationKillsProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/pids"
	t.Setenv("MAESTRO_PROCESS_TREE_PIDS", pidFile)
	t.Setenv("PATH", fakeBin(t, "codex", forkedLegacyScript))

	ctx, cancel := context.WithCancel(t.Context())
	large := strings.Repeat("confidential structured source\n", 16_384)
	if len(large) <= 128<<10 {
		t.Fatalf("large prompt fixture = %d bytes, want > 128 KiB", len(large))
	}
	ch, err := NewCodexAgent().Execute(ctx, large, Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	pids := waitForProcessTree(t, pidFile)
	t.Cleanup(func() { killTestProcessTree(pids) })

	cancel()
	if events := collectEventsBounded(t, ch, 3*time.Second); len(events) != 0 {
		t.Fatalf("cancelled blocked-stdin stream emitted stale events: %+v", events)
	}
	assertProcessesGone(t, pids, 2*time.Second)
}

func TestBlobCancellationKillsLegacyProcessTree(t *testing.T) {
	pidFile := t.TempDir() + "/pids"
	t.Setenv("MAESTRO_PROCESS_TREE_PIDS", pidFile)
	t.Setenv("PATH", fakeBin(t, "claude", forkedLegacyScript))

	type executeResult struct {
		ch  <-chan agentcore.StreamEvent
		err error
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan executeResult, 1)
	go func() {
		ch, err := NewClaudeAgent().Execute(ctx, "wait", Options{})
		result <- executeResult{ch: ch, err: err}
	}()

	pids := waitForProcessTree(t, pidFile)
	t.Cleanup(func() { killTestProcessTree(pids) })
	started := time.Now()
	cancel()

	var got executeResult
	select {
	case got = <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("blob agent did not return after cancellation")
	}
	if got.err != nil {
		t.Fatalf("Execute: %v", got.err)
	}
	if events := collectEventsBounded(t, got.ch, time.Second); len(events) != 0 {
		t.Fatalf("cancelled blob emitted stale events: %+v", events)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s, want <= 3s", elapsed)
	}
	assertProcessesGone(t, pids, 2*time.Second)
}

func TestStreamingCancellationKillsEscapedLegacyDescendant(t *testing.T) {
	pidFile := t.TempDir() + "/pids"
	readyFile := t.TempDir() + "/ready"
	t.Setenv("MAESTRO_PROCESS_TREE_PIDS", pidFile)
	t.Setenv("MAESTRO_ESCAPED_PROCESS_READY", readyFile)
	t.Setenv("MAESTRO_TEST_BINARY", os.Args[0])
	t.Setenv("PATH", fakeBin(t, "codex", escapedLegacyScript))

	ctx, cancel := context.WithCancel(t.Context())
	ch, err := NewCodexAgent().Execute(ctx, "wait", Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	pids := waitForProcessTree(t, pidFile)
	t.Cleanup(func() { killTestProcessTree(pids) })

	started := time.Now()
	cancel()
	events := collectEventsBounded(t, ch, 3*time.Second)
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s, want <= 3s", elapsed)
	}
	if len(events) != 0 {
		t.Fatalf("cancelled stream emitted stale events: %+v", events)
	}
	assertProcessesGone(t, pids, 2*time.Second)
}

func TestDescendantsOfHandlesDepthAndCycles(t *testing.T) {
	parents := map[int]int{
		20: 10,
		30: 20,
		40: 10,
		50: 50,
		60: 70,
		70: 60,
	}
	got := descendantsOf(10, parents)
	depths := make(map[int]int, len(got))
	for _, descendant := range got {
		depths[descendant.pid] = descendant.depth
	}
	want := map[int]int{20: 1, 30: 2, 40: 1}
	if len(depths) != len(want) {
		t.Fatalf("descendants = %+v, want %+v", depths, want)
	}
	for pid, depth := range want {
		if depths[pid] != depth {
			t.Errorf("depth[%d] = %d, want %d", pid, depths[pid], depth)
		}
	}
}

func TestLegacyInternalTimeoutRemainsAnError(t *testing.T) {
	t.Setenv("PATH", fakeBin(t, "codex", "sleep 60\n"))
	ch, err := lineStreamer(context.Background(), "codex", 50*time.Millisecond, "", parseCodexLine)
	if err != nil {
		t.Fatalf("lineStreamer: %v", err)
	}
	events := collectEventsBounded(t, ch, 3*time.Second)
	if len(events) != 1 || events[0].Type != agentcore.EvError {
		t.Fatalf("timeout events = %+v", events)
	}
	message := events[0].Content.(agentcore.StreamError).Message
	if !strings.Contains(message, "timed out after 50ms") {
		t.Fatalf("timeout message = %q", message)
	}
}

func waitForProcessTree(t *testing.T, path string) []int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var lastRaw []byte
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		if err == nil {
			fields := strings.Fields(string(raw))
			if len(fields) != 2 {
				// The shell creates/truncates the redirection target before its
				// builtin writes both lines. Under parallel test load, a reader can
				// observe that short-lived empty or partial file; wait for the
				// complete record instead of treating scheduler timing as a product
				// failure.
				lastRaw = append(lastRaw[:0], raw...)
				time.Sleep(10 * time.Millisecond)
				continue
			}
			pids := make([]int, 0, len(fields))
			for _, field := range fields {
				pid, convErr := strconv.Atoi(field)
				if convErr != nil || pid <= 1 {
					t.Fatalf("invalid test process PID %q", field)
				}
				pids = append(pids, pid)
			}
			return pids
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read PID file: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if lastRaw != nil {
		t.Fatalf("process PID file = %q, want parent and child", lastRaw)
	}
	t.Fatal("legacy test process did not start")
	return nil
}

func collectEventsBounded(t *testing.T, ch <-chan agentcore.StreamEvent, timeout time.Duration) []agentcore.StreamEvent {
	t.Helper()
	result := make(chan []agentcore.StreamEvent, 1)
	go func() {
		var events []agentcore.StreamEvent
		for ev := range ch {
			events = append(events, ev)
		}
		result <- events
	}()
	select {
	case events := <-result:
		return events
	case <-time.After(timeout):
		t.Fatal("event stream did not close after cancellation")
		return nil
	}
}

func assertProcessesGone(t *testing.T, pids []int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive := false
		for _, pid := range pids {
			if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
				alive = true
				break
			}
		}
		if !alive {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, pid := range pids {
		if err := syscall.Kill(pid, 0); err == nil || errors.Is(err, syscall.EPERM) {
			t.Errorf("legacy process PID %d survived cancellation", pid)
		}
	}
}

func killTestProcessTree(pids []int) {
	if len(pids) == 0 || pids[0] <= 1 {
		return
	}
	_ = syscall.Kill(-pids[0], syscall.SIGKILL)
	for _, pid := range pids {
		if pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}
}
