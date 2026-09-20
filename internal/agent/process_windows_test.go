//go:build windows

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/bryann2k/maestro/internal/agentcore"
)

const (
	windowsLegacyHelperMode = "MAESTRO_WINDOWS_LEGACY_HELPER"
	windowsLegacyPIDFile    = "MAESTRO_WINDOWS_LEGACY_PID_FILE"
)

func TestWindowsLegacyProcessHelper(t *testing.T) {
	switch os.Getenv(windowsLegacyHelperMode) {
	case "parent":
		child := exec.Command(os.Args[0], "-test.run=TestWindowsLegacyProcessHelper")
		child.Env = append(os.Environ(), windowsLegacyHelperMode+"=descendant")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			os.Getenv(windowsLegacyPIDFile),
			[]byte(strconv.Itoa(child.Process.Pid)),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	case "descendant":
		for {
			time.Sleep(time.Hour)
		}
	}
}

func TestStreamingCancellationKillsWindowsLegacyDescendant(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv(windowsLegacyHelperMode, "parent")
	t.Setenv(windowsLegacyPIDFile, pidFile)
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream, err := lineStreamer(ctx, binary, 30*time.Second, t.TempDir(), func(string) []agentcore.StreamEvent {
		return nil
	}, "-test.run=TestWindowsLegacyProcessHelper")
	if err != nil {
		t.Fatal(err)
	}
	descendantPID := waitForWindowsLegacyPID(t, pidFile)
	assertWindowsLegacyProcessRunning(t, descendantPID)
	cancel()
	waitForWindowsLegacyStream(t, stream)
	assertWindowsLegacyProcessExited(t, descendantPID)
}

func TestBlobCancellationKillsWindowsLegacyDescendant(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	t.Setenv(windowsLegacyHelperMode, "parent")
	t.Setenv(windowsLegacyPIDFile, pidFile)
	binary, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	type result struct {
		stream <-chan agentcore.StreamEvent
		err    error
	}
	done := make(chan result, 1)
	go func() {
		stream, runErr := blobParser(ctx, binary, 30*time.Second, t.TempDir(), func(string) []agentcore.StreamEvent {
			return nil
		}, "-test.run=TestWindowsLegacyProcessHelper")
		done <- result{stream: stream, err: runErr}
	}()
	descendantPID := waitForWindowsLegacyPID(t, pidFile)
	assertWindowsLegacyProcessRunning(t, descendantPID)
	cancel()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		waitForWindowsLegacyStream(t, got.stream)
	case <-time.After(5 * time.Second):
		t.Fatal("blob parser did not return after cancellation")
	}
	assertWindowsLegacyProcessExited(t, descendantPID)
}

func waitForWindowsLegacyPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(string(data))
			if parseErr != nil || pid <= 0 {
				t.Fatalf("invalid descendant PID %q: %v", data, parseErr)
			}
			return pid
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for legacy descendant PID")
	return 0
}

func waitForWindowsLegacyStream(t *testing.T, stream <-chan agentcore.StreamEvent) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for range stream {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("legacy event stream did not close")
	}
}

func assertWindowsLegacyProcessRunning(t *testing.T, pid int) {
	t.Helper()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open descendant %d: %v", pid, err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("descendant %d is not running: state %#x, error %v", pid, state, err)
	}
}

func assertWindowsLegacyProcessExited(t *testing.T, pid int) {
	t.Helper()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatalf("open descendant %d after cancellation: %v", pid, err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 5000)
	if err != nil || state != uint32(windows.WAIT_OBJECT_0) {
		t.Fatalf("descendant %d survived: state %#x, error %v", pid, state, err)
	}
}
