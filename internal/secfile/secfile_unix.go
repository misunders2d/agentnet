//go:build !windows

package secfile

import (
	"errors"
	"os"
)

func restrict(path string, dir bool) error {
	mode := os.FileMode(0o600)
	if dir {
		mode = 0o700
	}
	return os.Chmod(path, mode)
}

func check(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("insecure permissions: group/other access must be removed (chmod 600)")
	}
	return nil
}
