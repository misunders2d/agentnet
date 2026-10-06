package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGroupPendingPeopleRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_pending_rendered.cjs")
	// Screenshots are private test artifacts, never committed or uploaded.
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
