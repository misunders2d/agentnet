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
