package ui

import (
	"os/exec"
	"testing"
)

// TestComicHeldStatus runs Comic's held-back helpers (web/src/model.ts) in
// Node: an own verified device is named as yours, not "unverified sender";
// copies count as records (an estimate for older copies, said so); and each
// line says who can act, never asking a device that only claims an address.
func TestComicHeldStatus(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/comic_held_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
