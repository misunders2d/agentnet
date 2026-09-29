package client

import (
	"errors"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// fdWallTimer is a timerfd on CLOCK_REALTIME armed with an absolute time:
// the kernel checks it against the wall clock, so it fires on resume when
// the time passed while the computer was suspended, and with
// TFD_TIMER_CANCEL_ON_SET a read also returns when the clock is set, so the
// daemon looks again. It is read through the runtime poller (non-blocking
// fd), so Stop's close ends the read.
type fdWallTimer struct {
	f    *os.File
	c    chan struct{}
	done chan struct{}
}

func platformWallTimer() wallTimer {
	fd, err := unix.TimerfdCreate(unix.CLOCK_REALTIME, unix.TFD_CLOEXEC|unix.TFD_NONBLOCK)
	if err != nil {
		return newGoWallTimer()
	}
	f := os.NewFile(uintptr(fd), "reminder timer")
	if f.SetReadDeadline(time.Time{}) != nil { // not on the poller: a read would not block
		f.Close()
		return newGoWallTimer()
	}
	t := &fdWallTimer{f: f, c: make(chan struct{}, 1), done: make(chan struct{})}
	go t.read()
	return t
}

func (t *fdWallTimer) read() {
	defer close(t.done)
	buf := make([]byte, 8)
	for {
		_, err := t.f.Read(buf)
		if err != nil && !errors.Is(err, syscall.ECANCELED) {
			return // closed (Stop), or unusable
		}
		select {
		case t.c <- struct{}{}:
		default:
		}
	}
}

func (t *fdWallTimer) C() <-chan struct{} { return t.c }

func (t *fdWallTimer) Arm(at time.Time) error {
	var spec unix.ItimerSpec
	if !at.IsZero() {
		spec.Value = unix.NsecToTimespec(max(1, at.UnixNano())) // a past time fires at once; zero would disarm
	}
	sc, err := t.f.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := sc.Control(func(fd uintptr) {
		serr = unix.TimerfdSettime(int(fd), unix.TFD_TIMER_ABSTIME|unix.TFD_TIMER_CANCEL_ON_SET, &spec, nil)
	}); err != nil {
		return err
	}
	return serr
}

func (t *fdWallTimer) Stop() {
	t.f.Close()
	<-t.done
}
