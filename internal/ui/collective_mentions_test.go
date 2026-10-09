package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCollectiveMentionSelection(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node required")
	}
	for _, script := range []string{"collective_mentions_check.mjs", "teams_check.mjs"} {
		out, err := exec.CommandContext(t.Context(), "node", "testdata/"+script).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
	}
}

func TestCollectiveTagsComposerRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: installed Playwright required")
	}
	for _, mode := range []string{"AGENTNET_MULTI_AGENT_REGRESSION=1", "AGENTNET_TEAM_EDIT_REGRESSION=1"} {
		cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
		cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic,classic,zoom", mode, "AGENTNET_COLLECTIVE_TAGS=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), `"ok":true`) {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		t.Logf("%s", out)
	}
}
