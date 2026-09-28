package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A Task Scheduler query that hangs only makes the switch unavailable, and
// soon: the daemon keeps serving (client: a refused switch never stops it).
func TestWindowsSwitchCheckHangIsBounded(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_FAKE_BINARY", "1")
	old := runTask
	runTask = func(_ time.Duration, max int, _ string, _ ...string) ([]byte, error) {
		return runQuiet(300*time.Millisecond, max, self, "sleep") // stands in for a stuck schtasks or PowerShell
	}
	t.Cleanup(func() { runTask = old })
	canSwitch, _ := switchHooks(t.TempDir(), self)
	start := time.Now()
	ok, why := canSwitch()
	if ok || !strings.Contains(why, "did not finish within") {
		t.Fatalf("hanging query: ok=%v %q", ok, why)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("the check took %v", d)
	}
}
