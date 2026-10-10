package client

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"sync"
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
	return homeSockPath(home, "daemon.sock", ".sock")
}

// changesSockPath is the daemon's change socket (listenChanges), named by
// sockPath's rules: HOME/changes.sock, or HASH.changes.sock in the private
// per-user directory for a long home.
func changesSockPath(home string) string {
	return homeSockPath(home, "changes.sock", ".changes.sock")
}

func homeSockPath(home, name, suffix string) string {
	p := filepath.Join(home, name)
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
	return filepath.Join(dir, hex.EncodeToString(sum[:8])+suffix)
}

// notifyDaemon wakes a running daemon's worker, if there is one.
func notifyDaemon(home string) { dialDaemon(home) }

func dialDaemon(home string) bool {
	c, err := net.DialTimeout("unix", sockPath(home), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// notifyOwnWork wakes this home's daemon for work this program queued in
// the course of its own receiving, syncing or running: a job ready, history
// copies, a receiver record, statuses due. Sent by the daemon to itself it
// is a self-wake: it brings no new evidence for held messages (code that
// records evidence marks it itself, convWork), so it starts no other look
// at them. From any other process it is the ordinary wake.
func (a *Agent) notifyOwnWork() {
	if !a.kicksLive.Load() {
		notifyDaemon(a.home)
		return
	}
	a.selfKicks.Add(1)
	if !dialDaemon(a.home) {
		a.selfKicks.Add(-1)
	}
}

// ownKick reports whether a wake the socket served is one this daemon sent
// itself (notifyOwnWork), counting it as served. Wakes carry no data, so
// one from another process that comes in between may be counted instead;
// the wakes together still mark the same work.
func (a *Agent) ownKick() bool {
	for {
		n := a.selfKicks.Load()
		if n <= 0 {
			return false
		}
		if a.selfKicks.CompareAndSwap(n, n-1) {
			return true
		}
	}
}

// listenKicks forwards connections on the daemon socket to wake. The caller
// holds the daemon lock, so an existing socket file is stale. stop returns
// once no wake runs any more, so what its caller changes next is never
// raced by a wake still running.
func listenKicks(home string, wake func()) (stop func(), err error) {
	path := sockPath(home)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
			wake()
		}
	}()
	return func() { ln.Close(); <-done; os.Remove(path) }, nil
}

// listenChanges serves the daemon's change socket: an owner-only local
// socket on which each connection receives one byte at once and then one
// byte per local change (changed: the daemon's change feed), coalesced. It
// reads nothing, so a connection can learn only that something changed
// here, never what, and can make nothing happen. A command waiting for an
// answer (AwaitReply) blocks on it instead of polling. A write that does
// not complete within changesWriteTimeout ends that connection. stop
// returns once every connection is closed.
func listenChanges(home string, changed func() (uint64, <-chan struct{})) (stop func(), err error) {
	path := changesSockPath(home)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	os.Chmod(path, 0o600) // owner only, inside an owner-only directory as well
	quit := make(chan struct{})
	var wg sync.WaitGroup
	serve := func(c net.Conn) {
		defer wg.Done()
		defer c.Close()
		for {
			_, next := changed() // before writing, so no change is missed
			c.SetWriteDeadline(time.Now().Add(changesWriteTimeout))
			if _, err := c.Write([]byte{1}); err != nil {
				return
			}
			select {
			case <-next:
			case <-quit:
				return
			}
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go serve(c)
		}
	}()
	return func() { close(quit); ln.Close(); wg.Wait(); os.Remove(path) }, nil
}
