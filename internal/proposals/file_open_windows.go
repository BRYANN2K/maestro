//go:build windows

package proposals

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }
