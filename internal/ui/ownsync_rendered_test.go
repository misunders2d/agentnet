package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestComicOwnSyncRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	for _, tc := range []struct{ name, mode string }{
		{"synced_file", "AGENTNET_DIRECT_FILES_REGRESSION"},
		{"held_flood", "AGENTNET_HELD_FLOOD_REGRESSION"},
		{"topic_invite", "AGENTNET_TOPIC_INVITE_REGRESSION"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "node", "testdata/group_guest_controls_rendered.cjs")
			cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir(), "AGENTNET_TEST_SKINS=comic", tc.mode+"=1")
			out, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(out), `"ok":true`) {
				t.Fatalf("%v\n%s", err, out)
			}
			t.Logf("%s", out)
		})
	}
}
