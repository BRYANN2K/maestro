//go:build windows

package mcp

import (
	"errors"

	"golang.org/x/sys/windows"
)

func testProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false
	}
	if err != nil {
		return true
	}
	defer windows.CloseHandle(process)
	state, err := windows.WaitForSingleObject(process, 0)
	return err == nil && state == uint32(windows.WAIT_TIMEOUT)
}

func terminateTestProcess(pid int) {
	if pid <= 0 {
		return
	}
	process, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(process)
	_ = windows.TerminateProcess(process, 1)
}
