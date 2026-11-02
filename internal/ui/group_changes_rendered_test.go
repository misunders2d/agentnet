package ui

import (
	"os"
	"testing"
)

func TestGroupChangesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered group changes")
	}
	t.Log(browserCheck(t, "testdata/group_changes_rendered.cjs"))
}

func TestAgentDismissalsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered agent dismissals")
	}
	t.Log(browserCheck(t, "testdata/agent_dismissal_rendered.cjs"))
}
