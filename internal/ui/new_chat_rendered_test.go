package ui

import (
	"os"
	"testing"
)

func TestNewChatRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered new-chat creation")
	}
	t.Log(browserCheck(t, "testdata/new_chat_rendered.cjs"))
}
