package client

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"time"
)

// The daemon listens on an owner-only local socket. A connection carries no
// data: it only tells the worker to look at the inbox again (after accept,
// cancel, approve or a responder change made by another process). Nothing is
// read from it, so connecting cannot make anything run that the inbox does
// not already allow.

// maxSockPath keeps socket paths under the smallest sun_path limit (macOS
// allows 104 bytes including the terminator; Linux and Windows 108).
const maxSockPath = 100

// sockPath is HOME/daemon.sock when that fits the limit. For longer homes
// it is a per-home name in a private per-user directory under the system
// temporary directory, derived from the absolute home path so the daemon
// and CLI agree on it.
func sockPath(home string) string {
	p := filepath.Join(home, "daemon.sock")
	if len(p) <= maxSockPath {
		return p
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		abs = home
	}
	sum := sha256.Sum256([]byte(abs))
	dir, err := privateSockDir()
	if err != nil {
		return p // unusable: the listener fails and pings still wake the worker
	}
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock")
}

// notifyDaemon wakes a running daemon's worker, if there is one.
func notifyDaemon(home string) {
	if c, err := net.DialTimeout("unix", sockPath(home), time.Second); err == nil {
		c.Close()
	}
}

// listenKicks forwards connections on the daemon socket to wake. The caller
// holds the daemon lock, so an existing socket file is stale.
func listenKicks(home string, wake func()) (stop func(), err error) {
	path := sockPath(home)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
			wake()
		}
	}()
	return func() { ln.Close(); os.Remove(path) }, nil
}
