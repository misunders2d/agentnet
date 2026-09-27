// Package lockfile holds an exclusive OS lock on a file for the life of a
// process, so only one daemon runs per agent home. The OS releases the lock
// if the process dies.
package lockfile

import (
	"errors"
	"os"
	"sync"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("already locked by another process")

// Acquire locks path, creating it if needed, or fails with ErrLocked if
// another holder has it. Call the returned function to release it.
func Acquire(path string) (func(), error) { return acquire(path, false) }

// Wait is Acquire that blocks until the lock is free.
func Wait(path string) (func(), error) { return acquire(path, true) }

func acquire(path string, wait bool) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lock(f, wait); err != nil {
		f.Close()
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { unlock(f); f.Close() }) }, nil // safe to call twice
}
