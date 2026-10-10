package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBrowserNotificationViewUsesCachedSupport(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/notify_cached_view_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// A browser device shows its saved chats within bounds with the relay held or
// refused; with the skin catalog or Comic's document rules (fonts) held, both
// adopted when they arrive later; with the catalog and Comic's own manifest
// both answering after the loader's wait for the catalog (a slow link opens
// the page later, never fails it); and with an arrival told while Comic's
// first overview still loads, which Comic then shows.
func TestDeviceCachedStartupRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"held", "offline", "catalog-held", "catalog-slow", "document-held", "change-during-overview"} {
		t.Run(mode, func(t *testing.T) {
			out, err := exec.Command(node, "testdata/device_startup_rendered.cjs", mode).CombinedOutput()
			if err != nil || !strings.Contains(string(out), "cached device Comic real IndexedDB startup PASS ["+mode+"]") {
				t.Fatalf("%v\n%s", err, out)
			}
			t.Logf("%s", out)
		})
	}
}
