package client

import (
	"net"
	"os"
	"path/filepath"
	"time"
)

// The daemon listens on an owner-only local socket in the agent home. A
// connection carries no data: it only tells the worker to look at the inbox
// again (after accept, cancel, approve or a responder change made by another
// process). Nothing is read from it, so connecting cannot make anything run
// that the inbox does not already allow.

func sockPath(home string) string { return filepath.Join(home, "daemon.sock") }

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
