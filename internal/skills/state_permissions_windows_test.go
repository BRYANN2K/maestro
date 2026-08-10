//go:build windows

package skills

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsStatePermissionsAcceptSynthesizedModes(t *testing.T) {
	dir := t.TempDir()
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStateDirectoryPermissions(info); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateStateFilePermissions(info); err != nil {
		t.Fatal(err)
	}
}
