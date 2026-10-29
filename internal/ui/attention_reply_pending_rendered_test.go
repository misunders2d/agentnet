package ui

import (
	"os"
	"testing"
)

func TestAttentionReplyPendingRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in rendered exact attention reply pending regression")
	}
	runNodeCheck(t, "testdata/attention_reply_pending_check.cjs")
}
