package ui

import (
	"os/exec"
	"testing"
)

// TestBrowserWorkspaceIdentityEngine runs the browser engine's parity check
// for the workspace name, the kept agent devices and the device words.
func TestBrowserWorkspaceIdentityEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/workspace_identity_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
