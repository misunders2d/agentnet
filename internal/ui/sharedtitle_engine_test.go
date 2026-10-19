package ui

import "testing"

// Signed routes and existing human/group fixtures, without a model or browser.
func TestBrowserSharedTopicTitle(t *testing.T) {
	runNodeCheck(t, "testdata/sharedtitle_engine_check.mjs")
}

func TestBrowserConversationResources(t *testing.T) {
	runNodeCheck(t, "testdata/resources_engine_check.mjs")
}
