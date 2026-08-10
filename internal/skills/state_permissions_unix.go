//go:build !windows

package skills

import (
	"errors"
	"os"
)

func validateStateFilePermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("skill state permissions are too broad; expected 0600")
	}
	return nil
}

func validateStateDirectoryPermissions(info os.FileInfo) error {
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("skill state directory permissions are too broad; expected 0700")
	}
	return nil
}
