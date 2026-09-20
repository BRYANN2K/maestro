//go:build unix

package cockpit

import (
	"os"
	"syscall"
)

func openRootReadOnly(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
