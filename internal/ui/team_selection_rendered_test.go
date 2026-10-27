package ui

import (
	"os"
	"testing"
)

func TestTeamSelectionRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered people-list selection")
	}
	t.Log(browserCheck(t, "testdata/team_selection_rendered.cjs"))
}

func TestTeamCreationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered people-list creation")
	}
	t.Log(browserCheck(t, "testdata/team_creation_rendered.cjs"))
}
