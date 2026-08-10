//go:build !windows

package skills

import (
	"os"
	"testing"
)

func TestUnixStatePermissionsRejectBroadDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStateDirectoryPermissions(info); err == nil {
		t.Fatal("broad state directory permissions were accepted")
	}
}
