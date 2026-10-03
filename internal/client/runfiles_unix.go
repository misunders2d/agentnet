//go:build !windows

package client

import (
	"os"
	"syscall"
)

// outboxSupported: the outbox's file checks can be made here.
const outboxSupported = true

// openOutFlags open an outbox file without following a link or waiting on
// a FIFO swapped in after it was listed.
const openOutFlags = os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK

// statKey reads a file's identity, link count and owner.
func statKey(info os.FileInfo) (fileKey, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileKey{}, false
	}
	return fileKey{dev: uint64(st.Dev), ino: uint64(st.Ino), nlink: uint64(st.Nlink), uid: int(st.Uid)}, true
}
