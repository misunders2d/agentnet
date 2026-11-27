package ui

import "testing"

// Signed routes and existing human/group fixtures, without a model or browser.
func TestBrowserSharedTopicTitle(t *testing.T) {
	runNodeCheck(t, "testdata/sharedtitle_engine_check.mjs")
}

func TestBrowserConversationResources(t *testing.T) {
	runNodeCheck(t, "testdata/resources_engine_check.mjs")
}

// MEL-840: an ordinary turn, text and a file, in a group that is one
// person's alone on this one browser device.
func TestBrowserSoloGroupTurn(t *testing.T) {
	runNodeCheck(t, "testdata/solo_group_engine_check.mjs")
}
