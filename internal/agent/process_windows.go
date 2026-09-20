//go:build windows

package agent

import (
	"os/exec"
	"time"

	"github.com/bryann2k/maestro/internal/windowsjob"
)

const legacyProcessWaitDelay = 2 * time.Second

func startProcessTree(cmd *exec.Cmd) (processWaiter, error) {
	return windowsjob.Start(cmd, legacyProcessWaitDelay)
}

func runProcessTree(cmd *exec.Cmd) error {
	return windowsjob.Run(cmd, legacyProcessWaitDelay)
}
