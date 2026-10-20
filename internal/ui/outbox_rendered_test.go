package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Exercise the canonical production Comic package through Host API v1. The
// fixture is inert: no Hub, identity, model, or external network is involved.
func TestComicOutboxRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: installed Playwright required")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/outbox_rendered.cjs")
	evidence := os.Getenv("AGENTNET_OUTBOX_SCREENSHOTS")
	if evidence == "" {
		evidence = t.TempDir()
	}
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+evidence)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"ok":true`) || !strings.Contains(string(out), `"width":1280`) || !strings.Contains(string(out), `"width":390`) {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
