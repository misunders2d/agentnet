package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

// External commands the switch and the updater run are bounded in time and
// in output: a hang or a flood is an error, soon.
func TestRunQuietIsBounded(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_FAKE_BINARY", "1")
	t.Setenv("AGENTNET_FAKE_VERSION", "v1.2.3")
	start := time.Now()
	if _, err := runQuiet(300*time.Millisecond, 1<<10, self, "sleep"); err == nil || !strings.Contains(err.Error(), "did not finish within") {
		t.Fatalf("hang: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("a hanging command held the caller for %v", d)
	}
	if _, err := runQuiet(10*time.Second, 1<<20, self, "spew"); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("flood: %v", err)
	}
	out, err := runQuiet(10*time.Second, 1<<10, self, "version")
	if err != nil || !strings.HasPrefix(string(out), "agentnet v1.2.3 (protocol ") {
		t.Fatalf("normal: %q %v", out, err)
	}
}
