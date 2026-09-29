package notify

import (
	"os"
	"testing"
	"time"
	"unsafe"
)

// The struct matches NOTIFYICONDATAW as documented in shellapi.h.
func TestNotifyIconDataLayout(t *testing.T) {
	want := uintptr(976)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		want = 956
	}
	if got := unsafe.Sizeof(notifyIconData{}); got != want {
		t.Fatalf("sizeof NOTIFYICONDATAW = %d, want %d", got, want)
	}
}

// On CI the real API is called: it must succeed or fail cleanly within the
// timeout, and Close must remove what it added. This proves the calls, not
// that a person saw a notification. Skipped elsewhere so developer desktops
// are not shown test notifications.
func TestShowAndCloseOnCI(t *testing.T) {
	if os.Getenv("CI") != "true" {
		t.Skip("runs only on CI")
	}
	start := time.Now()
	err := Show("AgentNet test", "CI check; no action needed.")
	t.Logf("Show: %v after %s", err, time.Since(start))
	if time.Since(start) > requestTimeout+time.Second {
		t.Fatal("Show was not bounded")
	}
	if err == nil {
		if err := Show("AgentNet test", "replacing the first one."); err != nil {
			t.Fatalf("second Show: %v", err)
		}
	}
	Close()
	Close() // idempotent
}

// The structs match WNDCLASSEXW and MSG as documented in winuser.h.
func TestWindowStructLayouts(t *testing.T) {
	wc, m := uintptr(80), uintptr(48)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wc, m = 48, 28
	}
	if got := unsafe.Sizeof(wndClassEx{}); got != wc {
		t.Fatalf("sizeof WNDCLASSEXW = %d, want %d", got, wc)
	}
	if got := unsafe.Sizeof(msg{}); got != m {
		t.Fatalf("sizeof MSG = %d, want %d", got, m)
	}
}

// Natively, without showing anything: the owner thread's window and
// message loop take the icon's callback message, and only a click on the
// current notification's icon runs its action, once; a late click on a
// replaced one runs nothing. This proves the window procedure, the loop and
// the dispatch; that the Shell sends the click when a person clicks the
// banner needs a real desktop.
func TestCallbackMessageRunsClickOnce(t *testing.T) {
	if err := ensure(); err != nil {
		t.Skipf("no window here: %v", err)
	}
	ran := make(chan string, 2)
	send := func(event, id uint16) {
		procSendMessage.Call(window, msgIcon, 0, uintptr(event)|uintptr(id)<<16)
	}
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-ran:
			if got != want {
				t.Fatalf("ran %q, want %q", got, want)
			}
		case <-time.After(300 * time.Millisecond):
			if want != "" {
				t.Fatalf("%q did not run", want)
			}
		}
	}
	clicks.set(41, func() { ran <- "old" })
	clicks.set(42, func() { ran <- "new" }) // replaced
	send(ninBalloonTimeout, 42)
	send(ninBalloonHide, 42)
	send(ninBalloonUserClick, 41) // late click on the replaced one
	expect("")
	send(ninBalloonUserClick, 42)
	expect("new")
	send(ninBalloonUserClick, 42)
	expect("")
	clicks.set(0, nil)
}
