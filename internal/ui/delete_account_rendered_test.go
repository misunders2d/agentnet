package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDeleteAccountRendered: Delete account says "Deleting…" at once, holds
// the sheet while the relay works, says why on failure and shows the deleted
// state on success (Google Play account deletion policy).
func TestDeleteAccountRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered account deletion checks")
	}
	output, err := exec.CommandContext(t.Context(), "node", "testdata/delete_account_rendered.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "DeleteAccount PASS") {
		t.Fatalf("rendered account deletion: %v\n%s", err, output)
	}
	t.Log(string(output))
}
