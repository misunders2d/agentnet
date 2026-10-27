package ui

import (
	"os"
	"testing"
)

func TestDeviceLinkRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered device link recovery")
	}
	t.Log(browserCheck(t, "testdata/device_link_rendered.cjs"))
}
