package client

import (
	"runtime"
	"testing"
	"time"
)

// Both wall timers fire at the armed time (a past one at once), disarm on
// the zero time, take a new time in place of the old, and stop. That they
// also fire after a physical suspend (the Linux timerfd on CLOCK_REALTIME)
// is not shown by this test: only a native suspend and resume can show it.
func TestWallTimers(t *testing.T) {
	for name, make := range map[string]func() wallTimer{
		"platform (" + runtime.GOOS + ")": platformWallTimer,
		"go":                              func() wallTimer { return newGoWallTimer() },
	} {
		t.Run(name, func(t *testing.T) {
			w := make()
			defer w.Stop()
			fired := func(within time.Duration) bool {
				select {
				case <-w.C():
					return true
				case <-time.After(within):
					return false
				}
			}
			start := time.Now()
			if err := w.Arm(start.Add(200 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if !fired(2*time.Second) || time.Since(start) < 150*time.Millisecond {
				t.Fatalf("armed for 200ms: fired after %s", time.Since(start))
			}
			if err := w.Arm(time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := w.Arm(time.Now().Add(100 * time.Millisecond)); err != nil { // replaces the hour
				t.Fatal(err)
			}
			if !fired(2 * time.Second) {
				t.Fatal("the new time did not replace the old")
			}
			if err := w.Arm(time.Now().Add(100 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if err := w.Arm(time.Time{}); err != nil {
				t.Fatal(err)
			}
			if fired(400 * time.Millisecond) {
				t.Fatal("fired after being disarmed")
			}
			if err := w.Arm(time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			if !fired(time.Second) {
				t.Fatal("a past time did not fire at once")
			}
			// Far future (past 2262, where nanoseconds since 1970 overflow
			// int64): armed for then, or refused; never fired now.
			if err := w.Arm(time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil && fired(300*time.Millisecond) {
				t.Fatal("a year-2300 time fired at once")
			}
			if err := w.Arm(time.Now().Add(100 * time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if !fired(2 * time.Second) {
				t.Fatal("no longer fires after a far-future time")
			}
		})
	}
}
