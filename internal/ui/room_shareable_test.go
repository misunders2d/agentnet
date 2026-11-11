package ui

import (
	"os/exec"
	"testing"
)

// Owner, 2026-11-11: "why the fuck are agent messages not being shown to
// guests?" Bring someone in must offer the owner's own agents' replies and
// own other devices' messages as context.
func TestBringInOffersOwnAgentAndDeviceMessages(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for Comic room model checks")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/room_shareable_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
