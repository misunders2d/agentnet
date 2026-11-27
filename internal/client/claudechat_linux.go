package client

import (
	"errors"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// processExit is a pidfd for one exact process: done closes when it exits.
type processExit struct {
	fd, wake int
	done     chan struct{}
}

// watchProcessExit opens a pidfd for pid and checks, after opening, that it
// is still the process started at ticks; the wait blocks in poll, no timer.
func watchProcessExit(pid int, ticks uint64) (*processExit, error) {
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return nil, errors.New("the Claude chat process is gone")
	}
	if _, now, _, err := codexProcess(pid); err != nil || now != ticks {
		unix.Close(fd)
		return nil, errors.New("the Claude chat process changed")
	}
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		unix.Close(fd)
		return nil, err
	}
	e := &processExit{fd: fd, wake: p[1], done: make(chan struct{})}
	go func() {
		defer unix.Close(fd)
		defer unix.Close(p[0])
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}, {Fd: int32(p[0]), Events: unix.POLLIN}}
		for {
			_, err := unix.Poll(fds, -1)
			if err == unix.EINTR {
				continue
			}
			if err == nil && fds[0].Revents != 0 {
				close(e.done)
			}
			return
		}
	}()
	return e, nil
}

// close ends the wait; its poller closes the pidfd when it returns.
func (e *processExit) close() { unix.Close(e.wake) }

// peerPID is the connected peer's pid, after checking it is this user.
func peerPID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errors.New("not a unix socket")
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err = raw.Control(func(fd uintptr) { cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) }); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	if int(cred.Uid) != os.Getuid() {
		return 0, errors.New("peer is another user")
	}
	return int(cred.Pid), nil
}

func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}
