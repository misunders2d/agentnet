package ui

import (
	"os/exec"
	"testing"
)

func TestBrowserReceivePriorityBeforeKnownProof(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/receive_priority_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserReceivePriorityRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "receive_priority_check.mjs", "receivePriorityResult")
}
