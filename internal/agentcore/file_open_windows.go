//go:build windows

package agentcore

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }
