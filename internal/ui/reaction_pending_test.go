package ui

import (
	"os"
	"os/exec"
	"testing"
)

func TestReactionPendingRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for the synthetic rendered reaction fixture")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	cmd := exec.CommandContext(t.Context(), node, "testdata/reaction_pending_check.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rendered pending reactions: %v\n%s", err, output)
	}
	t.Log(string(output))
}
