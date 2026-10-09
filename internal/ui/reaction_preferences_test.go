package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReactionPreferences(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/reaction_preferences_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestReactionPreferencesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic,classic,zoom", "AGENTNET_REACTION_REGRESSION=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
