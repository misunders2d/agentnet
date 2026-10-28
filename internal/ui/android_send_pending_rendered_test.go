package ui

import (
	"os"
	"testing"
)

func TestAndroidSendPendingRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in Chromium Android-host send pending regression")
	}
	runNodeCheck(t, "testdata/android_send_pending_check.cjs")
}
