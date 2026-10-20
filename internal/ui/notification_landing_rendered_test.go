package ui

import (
	"os"
	"os/exec"
	"testing"
)

func TestComicNotificationLandingRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/notification_landing_rendered.cjs")
	cmd.Env = os.Environ()
	if os.Getenv("AGENTNET_SCREENSHOTS") == "" {
		cmd.Env = append(cmd.Env, "AGENTNET_SCREENSHOTS="+t.TempDir())
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
