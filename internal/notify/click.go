package notify

import "sync"

// Balloon notification events (shellapi.h): under NOTIFYICON_VERSION_4 the
// icon's callback message carries one in LOWORD(lParam) and the icon id in
// HIWORD(lParam). Only a click runs anything.
const (
	ninBalloonShow      = 0x0402 // WM_USER + 2
	ninBalloonHide      = 0x0403
	ninBalloonTimeout   = 0x0404
	ninBalloonUserClick = 0x0405 // "dismissed because the user clicked the mouse"
)

// The callback names the icon, not the notification, so each notification
// gets an icon of its own: its id is the notification's identity, and an id
// is never used twice in a process. Ids run 1..65535; once they are used up
// (65535 notifications in one daemon run), nextIconID gives 0 from then on
// and later notifications are shown without a click action (clicks come
// back when the daemon restarts). A callback for any id but the current
// one's is ignored, so a click on a replaced or closed notification,
// delivered late, never runs a newer one's action.
func nextIconID(last uint16) uint16 {
	if last == 0 || last == 65535 { // used up (0 stays 0)
		return 0
	}
	return last + 1
}

// firstIconID is the id of a process's first notification.
const firstIconID = 1

// clickTarget is the click action of the notification shown last (the icon
// with id). It runs once, and only when that notification is clicked: a
// newer notification replaces it, and a timeout, a hide, no click or a
// click on another icon runs nothing.
type clickTarget struct {
	mu sync.Mutex
	id uint16
	fn func()
}

func (c *clickTarget) set(id uint16, fn func()) {
	c.mu.Lock()
	c.id, c.fn = id, fn
	c.mu.Unlock()
}

// callback handles one icon callback's lParam: a click on the current
// notification's balloon runs its action on its own goroutine (never in the
// window procedure) and reports whether it did.
func (c *clickTarget) callback(lparam uintptr) bool {
	id := uint16(lparam >> 16)
	if uint16(lparam) != ninBalloonUserClick || id == 0 {
		return false
	}
	c.mu.Lock()
	fn := c.fn
	if id != c.id {
		fn = nil
	} else {
		c.fn = nil
	}
	c.mu.Unlock()
	if fn == nil {
		return false
	}
	go fn()
	return true
}
