package ui

import (
	"os/exec"
	"testing"
)

func TestGroupAgentRejoinProjection(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.Command(node, "testdata/rejoin_projection_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("rejoin projection: %v\n%s", err, out)
	}
}
