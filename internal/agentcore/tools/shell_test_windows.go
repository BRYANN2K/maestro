//go:build windows

package tools

import (
	"fmt"
	"strings"
)

func bashCancellationCommand(testExecutable string) string {
	quoted := strings.ReplaceAll(testExecutable, `"`, `""`)
	return fmt.Sprintf(
		`start "" /B "%s" -test.run=^TestBashCancellationHelper$ & ping 127.0.0.1 -n 30 >NUL`,
		quoted,
	)
}
