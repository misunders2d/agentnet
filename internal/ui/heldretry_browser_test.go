package ui

import (
	"os/exec"
	"testing"
)

// Receiver isolation in the browser engine: an unreadable history event is
// held (RS-2), the held retry pass survives a throwing row and continues
// where an unreachable server stopped it (RS-8), and availability-only
// member lists start no pass (FLOOD-1).
func TestBrowserHeldRetryIsolation(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/held_retry_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
