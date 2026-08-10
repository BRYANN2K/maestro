//go:build windows

package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

const legacyProcessWaitDelay = 2 * time.Second

// configureProcessTree gives the vendor CLI a distinct Windows process group
// and replaces CommandContext's parent-only kill with taskkill's tree kill.
// This is the portable fallback when Maestro is not installed as a service
// capable of assigning child processes to a Windows Job Object.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		ctx, cancel := context.WithTimeout(context.Background(), legacyProcessWaitDelay)
		defer cancel()
		kill := exec.CommandContext(ctx, "taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F")
		if err := kill.Run(); err == nil {
			return nil
		}
		err := cmd.Process.Kill()
		if errors.Is(err, os.ErrProcessDone) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = legacyProcessWaitDelay
}
