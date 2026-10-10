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

func TestDeviceCachedStartupRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "testdata/device_startup_rendered.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "cached device Comic real IndexedDB startup PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
