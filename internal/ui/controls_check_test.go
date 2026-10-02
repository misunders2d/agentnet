package ui

import (
	"os/exec"
	"testing"
)

// The browser engine's message controls (reactions, edits, deletions) in
// node against the same rules as the Go client: exact direction for equal
// ids, retraction of shared bytes, resolution order, refusal of an older
// peer. testdata/controls_check.mjs is the UI side's fixture.
func TestControlsLogic(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/controls_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
