package ui

import (
	"os/exec"
	"testing"
)

func TestDriveBrowserProvider(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Skip("node unavailable")
	}
	out, e := exec.Command(node, "testdata/drivespace_check.mjs").CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}
