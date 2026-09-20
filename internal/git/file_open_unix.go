//go:build unix

package git

import (
	"os"
	"syscall"
)

func openReadOnly(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
