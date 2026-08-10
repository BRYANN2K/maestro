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
