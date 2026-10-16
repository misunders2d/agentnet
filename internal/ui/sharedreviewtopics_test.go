package ui

import "testing"

func TestSharedReviewTopics(t *testing.T) {
	runNodeCheck(t, "testdata/shared_review_topics_check.mjs")
}
