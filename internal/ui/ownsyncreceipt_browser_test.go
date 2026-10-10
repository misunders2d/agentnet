package ui

import (
	"os/exec"
	"testing"
)

// MIXED-1: the browser engine receipts own-device sync carriers as
// delivered, and a relay's re-delivery is receipted again without being
// admitted or applied a second time.
func TestBrowserOwnSyncReceipts(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/ownsyncreceipt_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
