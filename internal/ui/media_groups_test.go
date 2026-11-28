package ui

import "testing"

// TestGroupedMediaLayout checks Comic's port of Telegram Desktop's album
// layout and grouping (MEL-856) against geometry from the unmodified C++.
func TestGroupedMediaLayout(t *testing.T) {
	runNodeCheck(t, "testdata/grouped_layout_check.mjs")
}
