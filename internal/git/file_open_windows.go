//go:build windows

package git

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }
