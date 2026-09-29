package notify

import (
	"errors"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows shows a notification-area balloon with Shell_NotifyIconW. Windows
// 10 and 11 show it as a banner attributed to this program. A balloon needs
// an icon in the notification area, and deleting the icon removes the
// balloon, so the icon stays from the first notification until Close.
//
// Clicks: the icon names a callback message (uCallbackMessage), which the
// Shell sends to our message-only window when something happens to the icon
// or its balloon; under NOTIFYICON_VERSION_4 LOWORD(lParam) says what
// (NIN_BALLOONUSERCLICK: "dismissed because the user clicked the mouse")
// and HIWORD(lParam) which icon. Each notification gets an icon of its own
// (click.go: nextIconID), so a late click on a replaced one is ignored and
// only a click on the current one runs its action. The window, its
// procedure, the message loop and every Shell_NotifyIconW call live on one
// locked OS thread: a window belongs to the thread that created it.

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procShellNotify      = shell32.NewProc("Shell_NotifyIconW")
	procRegisterClass    = user32.NewProc("RegisterClassExW")
	procCreateWindow     = user32.NewProc("CreateWindowExW")
	procDefWindowProc    = user32.NewProc("DefWindowProcW")
	procGetMessage       = user32.NewProc("GetMessageW")
	procDispatchMessage  = user32.NewProc("DispatchMessageW")
	procPostMessage      = user32.NewProc("PostMessageW")
	procSendMessage      = user32.NewProc("SendMessageW")
	procLoadIcon         = user32.NewProc("LoadIconW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

const (
	nimAdd          = 0
	nimModify       = 1
	nimDelete       = 2
	nimSetVersion   = 4
	nifMessage      = 0x01
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
	wmApp           = 0x8000
	msgRequest      = wmApp     // queued Show and Close requests are waiting
	msgIcon         = wmApp + 1 // the icon's callback message
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

// wndClassEx is WNDCLASSEXW (winuser.h).
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

// msg is MSG (winuser.h).
type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

type request struct {
	title, body string
	onClick     func()
	close       bool
	done        chan error
}

var (
	start    sync.Once
	started  = make(chan struct{}) // closed once the owner thread made its window (or failed)
	startErr error
	window   uintptr // the message-only window, set before started is closed
	queueMu  sync.Mutex
	queue    []request
	clicks   clickTarget
	iconID   uint16 // owner thread only: the current notification's icon, 0 for none
	lastID   uint16 // owner thread only: the last icon id given out (ids advance even when a notification fails)
)

// Show displays a silent balloon, replacing this process's previous one.
// Windows queues a balloon behind one still shown; the previous one's icon is
// deleted first, which removes its balloon (as documented).
func Show(title, body string) error { return ShowAction(title, body, nil, nil) }

// ShowAction is Show with a click: onClick runs (on its own goroutine) if
// the person clicks this balloon, not otherwise. argv is not used here (it
// is for desktops that keep a command with a notification).
func ShowAction(title, body string, argv []string, onClick func()) error {
	return send(request{title: title, body: body, onClick: onClick})
}

// Close removes the notification-area icon (and any balloon, and its click
// action) if Show added one. The window and its thread stay for the process.
func Close() {
	select {
	case <-started:
		if startErr == nil {
			send(request{close: true})
		}
	default: // never started: nothing to remove
	}
}

// ensure starts the owner thread once and waits for its window.
func ensure() error {
	start.Do(func() { go owner() })
	select {
	case <-started:
		return startErr
	case <-time.After(requestTimeout):
		return errors.New("notification window did not start")
	}
}

func send(r request) error {
	if err := ensure(); err != nil {
		return err
	}
	r.done = make(chan error, 1)
	queueMu.Lock()
	queue = append(queue, r)
	queueMu.Unlock()
	if ok, _, err := procPostMessage.Call(window, msgRequest, 0, 0); ok == 0 {
		return errors.New("PostMessageW: " + err.Error())
	}
	timeout := time.NewTimer(requestTimeout)
	defer timeout.Stop()
	select {
	case err := <-r.done:
		return err
	case <-timeout.C:
		return errors.New("Shell_NotifyIconW did not return")
	}
}

// owner runs on one locked OS thread for the life of the process: it makes
// the message-only window of its own class, then runs the message loop that
// delivers queued requests and the icon's callbacks to wndProc.
func owner() {
	runtime.LockOSThread()
	window, startErr = makeWindow()
	close(started)
	if startErr != nil {
		return
	}
	var m msg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 { // WM_QUIT, or an error: the loop cannot go on
			return
		}
		procDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func makeWindow() (uintptr, error) {
	instance, _, _ := procGetModuleHandleW.Call(0)
	name, _ := windows.UTF16PtrFromString("AgentNetNotify")
	wc := wndClassEx{WndProc: windows.NewCallback(wndProc), Instance: instance, ClassName: name}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if atom, _, err := procRegisterClass.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 && err != windows.ERROR_CLASS_ALREADY_EXISTS {
		return 0, errors.New("RegisterClassExW: " + err.Error())
	}
	w, _, err := procCreateWindow.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, instance, 0)
	if w == 0 {
		return 0, errors.New("CreateWindowExW: " + err.Error())
	}
	return w, nil
}

// wndProc runs on the owner thread.
func wndProc(hwnd, message, wparam, lparam uintptr) uintptr {
	switch message {
	case msgRequest:
		queueMu.Lock()
		rs := queue
		queue = nil
		queueMu.Unlock()
		for _, r := range rs {
			r.done <- handle(hwnd, r)
		}
		return 0
	case msgIcon:
		clicks.callback(lparam)
		return 0
	}
	ret, _, _ := procDefWindowProc.Call(hwnd, message, wparam, lparam)
	return ret
}

// handle does one request on the owner thread. A notification replaces the
// previous one's icon with one of its own (removing its balloon and its
// action), so the callback identifies the notification.
func handle(wnd uintptr, r request) error {
	clicks.set(0, nil)
	if iconID != 0 {
		shellNotify(nimDelete, &notifyIconData{Wnd: wnd, ID: uint32(iconID)})
	}
	if r.close {
		iconID = 0
		return nil
	}
	lastID = nextIconID(lastID)
	id := lastID
	iconID = 0
	ok := false
	for attempt := 0; attempt < 2 && !ok; attempt++ { // a second try re-adds a lost icon (e.g. Explorer restarted)
		shellNotify(nimDelete, &notifyIconData{Wnd: wnd, ID: uint32(id)})
		ok = addIcon(wnd, id) && balloon(wnd, id, r.title, r.body)
	}
	if !ok {
		shellNotify(nimDelete, &notifyIconData{Wnd: wnd, ID: uint32(id)})
		return errors.New(errNoNotifyArea)
	}
	iconID = id
	clicks.set(id, r.onClick)
	return nil
}

// addIcon adds the icon, with its callback message, without a balloon and
// then selects NOTIFYICON_VERSION_4, which must follow every successful
// NIM_ADD.
func addIcon(wnd uintptr, id uint16) bool {
	icon, _, _ := procLoadIcon.Call(0, idiInformation)
	nid := notifyIconData{Wnd: wnd, ID: uint32(id), Flags: nifMessage | nifIcon | nifTip | nifShowTip, CallbackMessage: msgIcon, Icon: icon}
	copyUTF16(nid.Tip[:], "AgentNet")
	if !shellNotify(nimAdd, &nid) {
		return false
	}
	nid.Version = iconVersion4
	if shellNotify(nimSetVersion, &nid) {
		return true
	}
	shellNotify(nimDelete, &notifyIconData{Wnd: wnd, ID: uint32(id)})
	return false
}

// balloon shows a silent balloon on icon id that respects quiet time (the
// previous notification's icon, and so its balloon, is gone already).
func balloon(wnd uintptr, id uint16, title, body string) bool {
	nid := notifyIconData{Wnd: wnd, ID: uint32(id), Flags: nifInfo, InfoFlags: niifInfo | niifNoSound | niifQuietTime}
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
