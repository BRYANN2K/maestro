//go:build unix

package tools

import (
	"context"
	"os/exec"
)

func newShellCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "bash", "-c", command), nil
}
