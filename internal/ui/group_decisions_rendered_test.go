package ui

import (
	"os"
	"testing"
)

func TestGroupDecisionsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered group decision recovery")
	}
	t.Log(browserCheck(t, "testdata/group_decisions_rendered.cjs"))
}
