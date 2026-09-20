//go:build unix

package orchestrator

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureReviewProcessTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
}

func runReviewProcessTreeCommand(cmd *exec.Cmd) error {
	configureReviewProcessTree(cmd)
	return cmd.Run()
}
