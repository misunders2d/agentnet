//go:build !windows

package lockfile

import (
	"errors"
	"os"
	"syscall"
)

func lock(f *os.File, wait bool) error {
	how := syscall.LOCK_EX | syscall.LOCK_NB
	if wait {
		how = syscall.LOCK_EX
	}
	err := syscall.Flock(int(f.Fd()), how)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

func unlock(f *os.File) { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
