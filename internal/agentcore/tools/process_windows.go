//go:build windows

package tools

import (
	"os"
	"os/exec"
	"time"

	"github.com/bryann2k/maestro/internal/windowsjob"
)

// runProcessTreeCommand starts the shell suspended and assigns it to a
// kill-on-close Job Object before allowing it to execute. Context cancellation
// therefore terminates every descendant, not only the direct shell process.
func runProcessTreeCommand(cmd *exec.Cmd) error {
	return windowsjob.Run(cmd, 2*time.Second)
}

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }
