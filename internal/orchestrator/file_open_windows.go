//go:build windows

package orchestrator

import "os"

func openReadOnly(path string) (*os.File, error) { return os.Open(path) }

func openRootReadOnly(root *os.Root, path string) (*os.File, error) { return root.Open(path) }
