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

// Ids are never used twice: after 65535 they are used up for good, and a
// click on the old first notification can never run a later one's action.
func TestIconIDsNeverReused(t *testing.T) {
	seen := map[uint16]bool{}
	id := uint16(firstIconID)
	for n := 0; id != 0; n++ {
		if seen[id] || n > 65535 {
			t.Fatalf("id %d given twice (after %d)", id, n)
		}
		seen[id] = true
		id = nextIconID(id)
	}
	if len(seen) != 65535 || nextIconID(0) != 0 {
		t.Fatalf("%d ids, then %d", len(seen), nextIconID(0))
	}
	var c clickTarget
	c.set(nextIconID(65535), func() { t.Error("ran") }) // used up: no identity
	if c.callback(lparam(ninBalloonUserClick, firstIconID)) || c.callback(lparam(ninBalloonUserClick, 0)) {
		t.Fatal("a click after the ids were used up ran something")
	}
}
