// Package secfile stores secrets in files that only the current user can access.
package secfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// EnsureDir creates dir when missing and restricts it to the current user.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return restrict(dir, true)
}

// Write atomically replaces path with data restricted to the current user.
func Write(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".secret-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	if err := restrict(name, false); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(path))
}

// CreateTemp creates a new temporary file in dir restricted to the current
// user before any data is written, whatever access dir itself grants.
func CreateTemp(dir, pattern string) (*os.File, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	if err := restrict(f.Name(), false); err != nil {
		f.Close()
		os.Remove(f.Name())
		return nil, err
	}
	return f, nil
}

// Touch creates path as an empty owner-only file if it does not exist, so
// that programs which later create it (such as SQLite) inherit the
// restriction. An existing file must already be owner-only.
func Touch(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		if err := check(path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return restrict(path, false)
}

// Read returns the contents of path after verifying that no other user can access it.
func Read(path string) ([]byte, error) {
	if err := check(path); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return os.ReadFile(path)
}
