//go:build windows

package session

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }
