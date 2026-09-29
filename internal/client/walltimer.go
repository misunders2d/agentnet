package client

import (
	"sync"
	"time"
)

// wallTimer fires once at a wall-clock time. A reminder is "at 15:00": it
// must come due when the clock says so, also after the computer slept past
// it, without waiting for a network event to wake the daemon.
type wallTimer interface {
	C() <-chan struct{}     // receives when the armed time came (or the system clock was set)
	Arm(at time.Time) error // replaces any earlier time; the zero time disarms
	Stop()
}

// newWallTimer makes the platform's wall timer: on Linux a timerfd on
// CLOCK_REALTIME with an absolute time, which the kernel fires on resume
// when that time passed during a suspend (walltimer_linux.go); elsewhere,
// or if that fails, a Go timer, which runs on the monotonic clock: a sleep
// can delay it until the daemon next wakes for another reason. Tests may
// replace it.
var newWallTimer = platformWallTimer

// goWallTimer is the portable wall timer.
type goWallTimer struct {
	mu sync.Mutex
	t  *time.Timer
	c  chan struct{}
}

func newGoWallTimer() *goWallTimer { return &goWallTimer{c: make(chan struct{}, 1)} }

func (g *goWallTimer) C() <-chan struct{} { return g.c }

func (g *goWallTimer) Arm(at time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.t != nil {
		g.t.Stop()
		g.t = nil
	}
	if at.IsZero() {
		return nil
	}
	g.t = time.AfterFunc(max(0, time.Until(at)), func() {
		select {
		case g.c <- struct{}{}:
		default:
		}
	})
	return nil
}

func (g *goWallTimer) Stop() { g.Arm(time.Time{}) }
