package notify

import (
	"testing"
	"time"
)

func lparam(event, id uint16) uintptr { return uintptr(event) | uintptr(id)<<16 }

// Only a click on the current notification's icon runs its action, once;
// other events run nothing; a late click on a replaced or closed
// notification never runs the newer one's action.
func TestClickTarget(t *testing.T) {
	var c clickTarget
	ran := make(chan string, 4)
	act := func(name string) func() { return func() { ran <- name } }
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-ran:
			if got != want {
				t.Fatalf("ran %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			if want != "" {
				t.Fatalf("%q did not run", want)
			}
		}
	}
	c.set(5, act("first DM"))
	for _, ev := range []uint16{ninBalloonShow, ninBalloonHide, ninBalloonTimeout, 0x0400 /* NIN_SELECT */} {
		if c.callback(lparam(ev, 5)) {
			t.Fatalf("event %#x ran the action", ev)
		}
	}
	c.set(6, act("second DM")) // replaced
	if c.callback(lparam(ninBalloonUserClick, 5)) {
		t.Fatal("a late click on the replaced notification ran something")
	}
	if !c.callback(lparam(ninBalloonUserClick, 6)) {
		t.Fatal("a click on the current notification did not run")
	}
	expect("second DM")
	if c.callback(lparam(ninBalloonUserClick, 6)) {
		t.Fatal("a second click ran it again")
	}
	c.set(7, act("third"))
	c.set(0, nil) // closed
	if c.callback(lparam(ninBalloonUserClick, 7)) || c.callback(lparam(ninBalloonUserClick, 0)) {
		t.Fatal("a click after close ran something")
	}
	expect("")
}

func TestNextIconIDWraps(t *testing.T) {
	for in, want := range map[uint16]uint16{0: 1, 1: 2, 65534: 65535, 65535: 1} {
		if got := nextIconID(in); got != want {
			t.Errorf("next(%d) = %d, want %d", in, got, want)
		}
	}
}
