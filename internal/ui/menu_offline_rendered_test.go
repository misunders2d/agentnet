package ui

import (
	"os"
	"os/exec"
	"testing"
)

func TestMenuOfflineRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for the isolated current-source menu/offline fixture")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	cmd := exec.CommandContext(t.Context(), node, "testdata/menu_offline_rendered.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rendered menu/offline: %v\n%s", err, output)
	}
	t.Log(string(output))
}
