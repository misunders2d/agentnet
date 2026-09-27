//go:build windows

package secfile

// SyncDir does nothing on Windows: flushing directory entries is not
// implemented here, so the durability of renames on Windows is unverified.
func SyncDir(dir string) error { return nil }
