//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package mcp

import (
	"context"
	"os/exec"
)

func configureProcessGroup(*exec.Cmd) {}

func newStdioCommand(_ context.Context, name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

func startProcessGroup(cmd *exec.Cmd) (processWaiter, error) {
	configureProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return commandWaiter{cmd: cmd}, nil
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
