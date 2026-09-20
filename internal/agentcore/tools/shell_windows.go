//go:build windows

package tools

import (
	"context"
	"os"
	"os/exec"
)

func newShellCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	name, args, err := windowsShellInvocation(
		os.Getenv("MAESTRO_SHELL"),
		os.Getenv("COMSPEC"),
		command,
	)
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, name, args...), nil
}
