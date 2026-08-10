//go:build !windows

package skills

import (
	"os"
	"path/filepath"
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

func TestStateLoadRepairsOwnedLegacyDirectoryPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewStateStore(dir, "repo-123")
	if _, err := store.Load(t.Context()); err != nil {
		t.Fatalf("Load legacy directory: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("repaired directory mode = %o, want 700", got)
	}
}
