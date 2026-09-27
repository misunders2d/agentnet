//go:build windows

package secfile

// SyncDir is a no-op on Windows: directory handles cannot be flushed there,
// and NTFS journals metadata changes such as renames. Durability of the
// rename itself is not independently verified on Windows.
func SyncDir(dir string) error { return nil }
