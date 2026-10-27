package ui

import (
	"os"
	"testing"
)

func TestPictureChangesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered profile picture recovery")
	}
	t.Log(browserCheck(t, "testdata/picture_changes_rendered.cjs"))
}
