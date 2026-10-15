package ui

import "testing"

func TestViewedTopicReadConvergesWithoutPush(t *testing.T) {
	runNodeCheck(t, "testdata/unread_refresh_check.mjs")
}
