//go:build !windows

package client

import (
	"errors"
	"os"
)

func checkNativeEvidencePermissions(path string, directory bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	want := os.FileMode(0600)
	if directory {
		want = 0700
	}
	if info.Mode().Perm() != want {
		return errors.New("existing private evidence permissions required")
	}
	return nil
}

func restrictNativeEvidenceFile(path string) error {
	// OpenFile already requested 0600. Keep the existing Unix mode contract.
	return checkNativeEvidencePermissions(path, false)
}

func shareNativeEvidenceDirectory(path string) error {
	return os.Chmod(path, 0755)
}
