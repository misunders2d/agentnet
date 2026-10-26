package ui

import (
	"os"
	"os/exec"
	"testing"
)

func TestLocalChangesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered local-change checks")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/local_changes_rendered.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rendered local changes: %v\n%s", err, output)
	}
	t.Log(string(output))
}

func TestTimelineNavigationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered navigation checks")
	}
	output, err := exec.CommandContext(t.Context(), "node", "testdata/timeline_navigation_rendered.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("rendered timeline navigation: %v\n%s", err, output)
	}
	t.Log(string(output))
}
