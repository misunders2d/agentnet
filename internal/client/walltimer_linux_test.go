package client

import "testing"

// On Linux the reminder timer is the timerfd, not the fallback.
func TestLinuxWallTimerIsTimerfd(t *testing.T) {
	w := platformWallTimer()
	defer w.Stop()
	if _, ok := w.(*fdWallTimer); !ok {
		t.Fatalf("platform wall timer is %T, want the timerfd", w)
	}
}
