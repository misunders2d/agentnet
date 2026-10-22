package ui

import "testing"

func TestComicUnreadChoices(t *testing.T) { runNodeCheck(t, "testdata/review_topics_ui_check.mjs") }

func TestTopicBarKeepsCurrentSelection(t *testing.T) {
	t.Setenv("AGENTNET_TOPIC_COMPACT_CHECK", "1")
	runNodeCheck(t, "testdata/topics_ui_check.cjs")
}
