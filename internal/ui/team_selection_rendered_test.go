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

func TestTeamChangesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered people-list changes")
	}
	t.Log(browserCheck(t, "testdata/team_changes_rendered.cjs"))
}

func TestTeamPeopleRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered list recipient selection")
	}
	t.Log(browserCheck(t, "testdata/team_people_rendered.cjs"))
}

func TestInviteOpenRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered invitation opening")
	}
	t.Log(browserCheck(t, "testdata/invite_open_rendered.cjs"))
}

func TestNewGroupRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered group creation")
	}
	t.Log(browserCheck(t, "testdata/new_group_rendered.cjs"))
}

func TestInvitationSendRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered invitation submission")
	}
	t.Log(browserCheck(t, "testdata/invitation_send_rendered.cjs"))
}

func TestPersonRenameRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered profile name changes")
	}
	t.Log(browserCheck(t, "testdata/person_rename_rendered.cjs"))
}

func TestPersonCreationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered person setup")
	}
	t.Log(browserCheck(t, "testdata/person_creation_rendered.cjs"))
}

func TestWorkspaceNameRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered workspace name changes")
	}
	t.Log(browserCheck(t, "testdata/workspace_name_rendered.cjs"))
}

func TestGoogleAccessRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered workspace access changes")
	}
	t.Log(browserCheck(t, "testdata/google_access_rendered.cjs"))
}
