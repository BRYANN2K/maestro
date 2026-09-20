//go:build windows

package orchestrator

import (
	"os/exec"
	"time"

	"github.com/bryann2k/maestro/internal/windowsjob"
)

func runReviewProcessTreeCommand(cmd *exec.Cmd) error {
	return windowsjob.Run(cmd, 2*time.Second)
}
