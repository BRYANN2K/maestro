//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package agent

import (
	"os/exec"
	"time"
)

const legacyProcessWaitDelay = 2 * time.Second

// configureProcessTree retains CommandContext's direct-child cancellation on
// platforms without process groups, while bounding inherited-pipe waits.
func configureProcessTree(cmd *exec.Cmd) {
	cmd.WaitDelay = legacyProcessWaitDelay
}

func startProcessTree(cmd *exec.Cmd) (processWaiter, error) {
	configureProcessTree(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return commandWaiter{cmd: cmd}, nil
}

func runProcessTree(cmd *exec.Cmd) error {
	configureProcessTree(cmd)
	return cmd.Run()
}
