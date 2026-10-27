package ui

import (
	"os"
	"testing"
)

func TestReviewUnreadRetainsPresentationRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in Chromium rendered Review regression")
	}
	runNodeCheck(t, "testdata/review_unread_rendered.cjs")
}
