//go:build unix

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureProcessTree puts the shell and every descendant in a fresh
// process group. CommandContext can then cancel the complete tool tree rather
// than leaving background children alive with inherited output descriptors.
func configureProcessTree(cmd *exec.Cmd) {
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

func runProcessTreeCommand(cmd *exec.Cmd) error {
	configureProcessTree(cmd)
	return cmd.Run()
}

// openReadOnly prevents a FIFO swapped into place between validation and open
// from blocking an agent turn forever. O_NONBLOCK has no effect on ordinary
// regular-file reads.
func openReadOnly(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
