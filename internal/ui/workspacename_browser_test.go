package ui

import (
	"os/exec"
	"testing"
)

func TestWorkspaceNameBrowserPersistenceAndTransport(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/workspace_name_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
