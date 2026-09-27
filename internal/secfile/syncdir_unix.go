//go:build !windows

package secfile

import "os"

// SyncDir flushes a directory so renames and new entries in it survive a crash.
func SyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}
