// Package blobfile holds the file operations behind resumable ciphertext
// uploads, shared by the Hub and by daemons accepting direct deliveries.
package blobfile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Errors from Check.
var (
	ErrIncomplete = errors.New("upload incomplete")
	ErrDigest     = errors.New("uploaded bytes do not match the reserved digest")
)

// WriteChunk durably writes data at offset, first dropping anything past
// offset left by an interrupted write.
func WriteChunk(path string, offset int64, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err = f.Truncate(offset); err == nil {
		if _, err = f.WriteAt(data, offset); err == nil {
			err = f.Sync()
		}
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Digest returns a file's length and hex SHA-256, streaming.
func Digest(path string) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return n, hex.EncodeToString(h.Sum(nil)), err
}

// Check verifies a file against the reserved size and digest.
func Check(path string, size int64, sha string) error {
	n, sum, err := Digest(path)
	switch {
	case err != nil:
		return err
	case n != size:
		return ErrIncomplete
	case sum != sha:
		return ErrDigest
	}
	return nil
}

// Promote renames part to final (unless an earlier attempt already did) and
// syncs the directory, so the caller may then record the file as stored.
func Promote(part, final string, syncDir func(string) error) error {
	if err := os.Rename(part, final); err != nil {
		if _, statErr := os.Stat(final); statErr != nil || !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncDir(filepath.Dir(final))
}

// RemoveIfExists deletes paths, ignoring ones already gone.
func RemoveIfExists(paths ...string) error {
	for _, p := range paths {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
