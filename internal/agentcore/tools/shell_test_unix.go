//go:build unix

package tools

import "strings"

func bashCancellationCommand(testExecutable string) string {
	quoted := "'" + strings.ReplaceAll(testExecutable, "'", `'"'"'`) + "'"
	return quoted + " -test.run=^TestBashCancellationHelper$ & wait"
}
