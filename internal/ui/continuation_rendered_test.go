package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Captured synthetic API only: never a harness, live grant or real group decision.
func continuationRendered(t *testing.T, mode string) {
	t.Helper()
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic,classic,zoom", mode+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func TestNeedsHumanContinuationRendered(t *testing.T) {
	continuationRendered(t, "AGENTNET_CONTINUATION_REGRESSION")
}

func TestPendingStaleInvitationDeclineRendered(t *testing.T) {
	continuationRendered(t, "AGENTNET_DECLINE_INVITATION_REGRESSION")
}

func TestLongLinkLabelsRendered(t *testing.T) {
	continuationRendered(t, "AGENTNET_LONG_LINK_REGRESSION")
}
