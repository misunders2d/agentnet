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
	nifIcon         = 0x02
	nifTip          = 0x04
	nifInfo         = 0x10
	niifInfo        = 0x01
	niifNoSound     = 0x10
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
		icon, _, _ := procLoadIcon.Call(0, idiInformation)
		nid := notifyIconData{Wnd: wnd, ID: iconID, Flags: nifIcon | nifTip | nifInfo, Icon: icon, InfoFlags: niifInfo | niifNoSound}
		copyUTF16(nid.Tip[:], "AgentNet")
		copyUTF16(nid.InfoTitle[:], r.title)
		copyUTF16(nid.Info[:], r.body)
		ok := added && shellNotify(nimModify, &nid)
		if !ok { // first notification, or the icon was lost (e.g. Explorer restarted)
			shellNotify(nimDelete, &notifyIconData{Wnd: wnd, ID: iconID})
			ok = shellNotify(nimAdd, &nid)
		}
		added = ok
		if !ok {
			r.done <- errors.New(errNoNotifyArea)
			continue
		}
		r.done <- nil
	}
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
