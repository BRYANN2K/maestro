//go:build windows

package skills

import "os"

// Go synthesizes 0666/0777 permission bits on Windows, and Chmod only changes
// the read-only attribute. POSIX group/other-bit validation would therefore
// reject every valid state store. Windows confidentiality relies on inherited
// ACLs of the user's private state directory; root confinement and symlink
// checks remain platform-independent in state.go.
func validateStateFilePermissions(os.FileInfo) error { return nil }

func validateStateDirectoryPermissions(os.FileInfo) error { return nil }
