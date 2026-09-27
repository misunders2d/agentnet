package notify

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows shows a notification-area balloon with Shell_NotifyIconW. Windows
// 10 and 11 show it as a banner attributed to this program. A balloon needs
// an icon in the notification area, and deleting the icon removes the
// balloon, so the icon stays from the first notification until Close.
// Windows queues a balloon behind one still shown, so each new one first
// removes the previous one (empty szInfo, as documented).

var (
	shell32           = windows.NewLazySystemDLL("shell32.dll")
	user32            = windows.NewLazySystemDLL("user32.dll")
	procShellNotify   = shell32.NewProc("Shell_NotifyIconW")
	procCreateWindow  = user32.NewProc("CreateWindowExW")
	procDestroyWindow = user32.NewProc("DestroyWindow")
	procLoadIcon      = user32.NewProc("LoadIconW")
)

const (
	nimAdd          = 0
	nimModify       = 1
	nimDelete       = 2
	nimSetVersion   = 4
	nifIcon         = 0x02
	nifTip          = 0x04
	nifInfo         = 0x10
	nifShowTip      = 0x80 // keep the standard tooltip under version 4
	niifInfo        = 0x01
	niifNoSound     = 0x10
	niifQuietTime   = 0x80 // NIIF_RESPECT_QUIET_TIME
	iconVersion4    = 4    // NOTIFYICON_VERSION_4
	idiInformation  = 32516
	hwndMessage     = ^uintptr(2) // HWND_MESSAGE, (HWND)-3
	iconID          = 1
	requestTimeout  = 10 * time.Second
	errNoNotifyArea = "Shell_NotifyIconW failed (no notification area: is a desktop session running?)"
)

// notifyIconData is NOTIFYICONDATAW (shellapi.h).
type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32 // union with uTimeout, which Windows ignores since Vista
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        windows.GUID
	BalloonIcon     uintptr
}

type request struct {
	title, body string
	close       bool
	done        chan error
}

var (
	start    sync.Once
	started  atomic.Bool
	requests = make(chan request)
)

// Show displays a silent balloon, replacing this process's previous one.
func Show(title, body string) error {
	start.Do(func() {
		started.Store(true)
		go owner()
	})
	return send(request{title: title, body: body})
}

// Close removes the notification-area icon (and any balloon) if Show added
// one.
func Close() {
	if started.Load() {
		send(request{close: true})
	}
}

func send(r request) error {
	r.done = make(chan error, 1)
	timeout := time.NewTimer(requestTimeout)
	defer timeout.Stop()
	select {
	case requests <- r:
	case <-timeout.C:
		return errors.New("notification area is busy")
	}
	select {
	case err := <-r.done:
		return err
	case <-timeout.C:
		return errors.New("Shell_NotifyIconW did not return")
	}
}

// owner runs on one locked OS thread for the life of the process: a window
// belongs to the thread that created it. The window is message-only and the
// icon asks for no callbacks, so nothing is ever sent to it and no message
// loop is needed.
func owner() {
	runtime.LockOSThread()
	var wnd uintptr
	added := false
	for r := range requests {
		if r.close {
			if added {
				nid := notifyIconData{Wnd: wnd, ID: iconID}
				shellNotify(nimDelete, &nid)
				added = false
			}
			if wnd != 0 {
				procDestroyWindow.Call(wnd)
				wnd = 0
			}
			r.done <- nil
			continue
		}
		if wnd == 0 {
			static, _ := windows.UTF16PtrFromString("STATIC")
			w, _, err := procCreateWindow.Call(0, uintptr(unsafe.Pointer(static)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, 0, 0)
			if w == 0 {
				r.done <- errors.New("CreateWindowExW: " + err.Error())
				continue
			}
			wnd = w
		}
		ok := false
		for attempt := 0; attempt < 2 && !ok; attempt++ { // a second try re-adds a lost icon (e.g. Explorer restarted)
			if !added {
				shellNotify(nimDelete, &notifyIconData{Wnd: wnd, ID: iconID})
				added = addIcon(wnd)
			}
			ok = added && balloon(wnd, r.title, r.body)
			if !ok {
				added = false
			}
		}
		if !ok {
			r.done <- errors.New(errNoNotifyArea)
			continue
		}
		r.done <- nil
	}
}

// addIcon adds the icon without a balloon and then selects
// NOTIFYICON_VERSION_4, which must follow every successful NIM_ADD.
func addIcon(wnd uintptr) bool {
	icon, _, _ := procLoadIcon.Call(0, idiInformation)
	nid := notifyIconData{Wnd: wnd, ID: iconID, Flags: nifIcon | nifTip | nifShowTip, Icon: icon}
	copyUTF16(nid.Tip[:], "AgentNet")
	if !shellNotify(nimAdd, &nid) {
		return false
	}
	nid.Version = iconVersion4
	if shellNotify(nimSetVersion, &nid) {
		return true
	}
	shellNotify(nimDelete, &notifyIconData{Wnd: wnd, ID: iconID})
	return false
}

// balloon removes any balloon still shown, so the new one is not queued
// behind it, then shows a silent one that respects quiet time.
func balloon(wnd uintptr, title, body string) bool {
	shellNotify(nimModify, &notifyIconData{Wnd: wnd, ID: iconID, Flags: nifInfo}) // empty szInfo
	nid := notifyIconData{Wnd: wnd, ID: iconID, Flags: nifInfo, InfoFlags: niifInfo | niifNoSound | niifQuietTime}
	copyUTF16(nid.InfoTitle[:], title)
	copyUTF16(nid.Info[:], body)
	return shellNotify(nimModify, &nid)
}

func shellNotify(msg uintptr, nid *notifyIconData) bool {
	nid.Size = uint32(unsafe.Sizeof(*nid))
	ret, _, _ := procShellNotify.Call(msg, uintptr(unsafe.Pointer(nid)))
	return ret != 0
}

// copyUTF16 copies s into dst, truncated to leave the terminating NUL.
func copyUTF16(dst []uint16, s string) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return
	}
	if len(u) > len(dst) {
		u = u[:len(dst)]
	}
	copy(dst, u)
	dst[len(dst)-1] = 0
}
