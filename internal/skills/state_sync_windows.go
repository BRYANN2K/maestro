//go:build windows

package skills

import "os"

// Windows does not expose a portable directory-fsync primitive through os.
// The temporary state file itself is flushed before the atomic rename.
func syncStateDirectory(_ *os.Root) error { return nil }
