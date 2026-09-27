// Package lockfile holds an exclusive OS lock on a file for the life of a
// process, so only one daemon runs per agent home. The OS releases the lock
// if the process dies.
package lockfile

import (
	"errors"
	"os"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("already locked by another process")

// Acquire locks path, creating it if needed. Call the returned function to
// release it.
func Acquire(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { unlock(f); f.Close() }, nil
}
