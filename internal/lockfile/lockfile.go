// Package lockfile holds an exclusive OS lock on a file for the life of a
// process, so only one daemon runs per agent home. The OS releases the lock
// if the process dies.
//
// The lock itself is github.com/gofrs/flock (BSD-3-Clause): flock(2) on
// Unix, LockFileEx on Windows, one byte at offset 0 either way; the file is
// created owner-only (0600) if missing and is never removed here.
package lockfile

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// ErrLocked means another process holds the lock.
var ErrLocked = errors.New("already locked by another process")

// Acquire locks path, creating it if needed, or fails with ErrLocked if
// another holder has it. Call the returned function to release it.
func Acquire(path string) (func(), error) { return acquire(path, false) }

// Wait is Acquire that blocks until the lock is free.
func Wait(path string) (func(), error) { return acquire(path, true) }

// AcquireWithin is Acquire that keeps trying for up to d, so a holder that
// only checks the lock for an instant does not cause a refusal. It fails
// with ErrLocked if the lock is still held after d.
func AcquireWithin(path string, d time.Duration) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	f := flock.New(path)
	ok, err := f.TryLockContext(ctx, 10*time.Millisecond)
	if !ok {
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			return nil, ErrLocked
		}
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { f.Unlock() }) }, nil
}

func acquire(path string, wait bool) (func(), error) {
	f := flock.New(path) // O_CREATE|O_RDONLY (O_RDWR where exclusive locks need it), 0600
	if wait {
		if err := f.Lock(); err != nil { // LOCK_EX / LOCKFILE_EXCLUSIVE_LOCK, blocking
			return nil, err
		}
	} else {
		ok, err := f.TryLock() // LOCK_NB / LOCKFILE_FAIL_IMMEDIATELY; a refusal closes the descriptor
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrLocked
		}
	}
	var once sync.Once
	return func() { once.Do(func() { f.Unlock() }) }, nil // LOCK_UN then close; safe to call twice
}
