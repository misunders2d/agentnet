//go:build linux

package client

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// listenRunOrigin serves <home>/run-origin.sock from this daemon process
// (runorigin.go). Holding the daemon lock proves an existing socket stale.
// Its path is fixed, since the asker is configured with it; the home must
// be owner-only.
func (a *Agent) listenRunOrigin() (func(), error) {
	home, err := filepath.Abs(a.home)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, runOriginFile)
	if len(path) > maxSockPath {
		return nil, fmt.Errorf("%s: path too long for a socket", path)
	}
	info, err := os.Stat(home)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return nil, fmt.Errorf("%s is writable by others; make it owner-only (chmod 700)", home)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	o := &runOrigins{byPID: map[int]runOrigin{}}
	a.runOrigin.Store(o)
	self := uint32(os.Getuid())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				time.Sleep(10 * time.Millisecond)
				continue
			}
			if uid, ok := peerUID(c); !ok || !runOriginPeerAllowed(uid, self) {
				c.Close() // no detail for other users
				continue
			}
			c.SetDeadline(time.Now().Add(runOriginTimeout))
			a.serveRunOriginConn(o, c, procLive)
		}
	}()
	a.Logf("run origin socket: %s", path)
	return func() {
		a.runOrigin.Store(nil)
		ln.Close()
		<-done
		os.Remove(path)
	}, nil
}

func peerUID(c *net.UnixConn) (uint32, bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}

// procLive reports process pid's parent and start time (/proc/PID/stat
// fields 4 and 22) and whether it still runs (not a zombie or dead).
func procLive(pid int) (ppid int, start uint64, alive bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	i := bytes.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(string(data[i+1:]))
	if len(f) < 20 {
		return 0, 0, false
	}
	ppid, err1 := strconv.Atoi(f[1])
	start, err2 := strconv.ParseUint(f[19], 10, 64)
	if err1 != nil || err2 != nil || f[0] == "Z" || f[0] == "X" || f[0] == "x" {
		return 0, 0, false
	}
	return ppid, start, true
}
