package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestOwnTaskPermissionsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for the bundled Comic permission flow")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	out, err := exec.Command(node, "testdata/own_tasks_rendered.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Own task permissions rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
