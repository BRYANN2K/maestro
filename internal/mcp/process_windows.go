//go:build windows

package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"time"

	"github.com/bryann2k/maestro/internal/windowsjob"
)

const mcpProcessWaitDelay = 2 * time.Second

func newStdioCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	return exec.CommandContext(ctx, name, args...)
}

func startProcessGroup(cmd *exec.Cmd) (processWaiter, error) {
	return windowsjob.Start(cmd, mcpProcessWaitDelay)
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if cmd.Cancel != nil {
		if err := cmd.Cancel(); err == nil || errors.Is(err, os.ErrProcessDone) {
			return
		}
	}
	_ = cmd.Process.Kill()
}
