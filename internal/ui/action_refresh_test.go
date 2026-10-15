package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestActionCompletionWaitsOnlyForSelectedView(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	script := os.Getenv("AGENTNET_ACTION_REFRESH_CHECK")
	if script == "" {
		script = "testdata/action_refresh_check.mjs"
	}
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, script)
	cmd.Env = append(os.Environ(), "AGENTNET_SOURCE="+source)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
