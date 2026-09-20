//go:build windows

package windowsjob

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const (
	windowsJobHelperMode = "MAESTRO_WINDOWS_JOB_HELPER"
	windowsJobPIDFile    = "MAESTRO_WINDOWS_JOB_PID_FILE"
)

func TestRunKillsDescendantTree(t *testing.T) {
	pidFile := t.TempDir() + `\descendant.pid`
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=TestWindowsJobHelperProcess")
	cmd.Env = append(os.Environ(), windowsJobHelperMode+"=parent", windowsJobPIDFile+"="+pidFile)
	done := make(chan error, 1)
	go func() { done <- Run(cmd, 2*time.Second) }()

	descendantPID := waitForPIDFile(t, pidFile, 5*time.Second)
	assertProcessRunning(t, descendantPID)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled job command returned no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job command did not return after cancellation")
	}
	assertProcessExited(t, descendantPID, 5*time.Second)
}

func TestStartSupportsStreamingAndKillsDescendantTree(t *testing.T) {
	pidFile := t.TempDir() + `\descendant.pid`
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestWindowsJobHelperProcess")
	cmd.Env = append(os.Environ(), windowsJobHelperMode+"=parent", windowsJobPIDFile+"="+pidFile)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}

	controller, err := Start(cmd, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = controller.Kill()
		_ = controller.Wait()
	})
	scanner := bufio.NewScanner(stdout)
	streamed := make(chan string, 1)
	go func() {
		if scanner.Scan() {
			streamed <- scanner.Text()
			return
		}
		streamed <- ""
	}()
	select {
	case line := <-streamed:
		if line != "ready" {
			t.Fatalf("streamed line = %q, error %v", line, scanner.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for streamed output")
	}
	descendantPID := waitForPIDFile(t, pidFile, 5*time.Second)
	assertProcessRunning(t, descendantPID)
	if err := controller.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := controller.Wait(); err == nil {
		t.Fatal("killed controller returned no wait error")
	}
	assertProcessExited(t, descendantPID, 5*time.Second)
}

func TestRootExitKillsBackgroundDescendant(t *testing.T) {
	pidFile := t.TempDir() + `\descendant.pid`
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=TestWindowsJobHelperProcess")
	cmd.Env = append(os.Environ(), windowsJobHelperMode+"=root-exit", windowsJobPIDFile+"="+pidFile)
	controller, err := Start(cmd, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = controller.Kill()
		_ = controller.Wait()
	})
	descendantPID := waitForPIDFile(t, pidFile, 5*time.Second)
	// Do not call Wait until the descendant is gone: this specifically proves
	// that the root-process watcher closes the job on root exit.
	assertProcessExited(t, descendantPID, 5*time.Second)
	if err := controller.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestWindowsJobHelperProcess(t *testing.T) {
	switch os.Getenv(windowsJobHelperMode) {
	case "parent", "root-exit":
		child := exec.Command(os.Args[0], "-test.run=TestWindowsJobHelperProcess")
		child.Env = append(os.Environ(), windowsJobHelperMode+"=descendant")
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			os.Getenv(windowsJobPIDFile),
			[]byte(strconv.Itoa(child.Process.Pid)),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if os.Getenv(windowsJobHelperMode) == "root-exit" {
			return
		}
		_, _ = os.Stdout.WriteString("ready\n")
		for {
			time.Sleep(time.Hour)
		}
	case "descendant":
		for {
			time.Sleep(time.Hour)
		}
	}
}

func waitForPIDFile(t *testing.T, path string, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
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
	t.Fatal("timed out waiting for descendant PID")
	return 0
}

func assertProcessRunning(t *testing.T, pid int) {
	t.Helper()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		t.Fatalf("open descendant %d: %v", pid, err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 0)
	if err != nil {
		t.Fatalf("query descendant %d: %v", pid, err)
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("descendant %d exited before cancellation (wait state %#x)", pid, state)
	}
}

func assertProcessExited(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return
	}
	if err != nil {
		t.Fatalf("open descendant %d after cancellation: %v", pid, err)
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, uint32(timeout/time.Millisecond))
	if err != nil {
		t.Fatalf("wait for descendant %d: %v", pid, err)
	}
	if state != uint32(windows.WAIT_OBJECT_0) {
		t.Fatalf("descendant %d survived cancellation (wait state %#x)", pid, state)
	}
}
