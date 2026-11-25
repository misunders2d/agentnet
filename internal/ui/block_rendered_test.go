package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestBlockRendered: Block and Unblock change the chat at once, a stale
// overview cannot undo a pending block, and a failure restores the chat and
// says why (Google Play user-generated content policy).
func TestBlockRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered block checks")
	}
	output, err := exec.CommandContext(t.Context(), "node", "testdata/block_rendered.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Block PASS") {
		t.Fatalf("rendered block: %v\n%s", err, output)
	}
	t.Log(string(output))
}
