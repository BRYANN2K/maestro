//go:build windows

package cockpit

import "os"

func openRootReadOnly(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
