package ui

import "testing"

func TestSharedVisibleRows(t *testing.T) {
	runNodeCheck(t, "testdata/shared_visible_rows_check.mjs")
}

func TestSharedNativeActivity(t *testing.T) {
	runNodeCheck(t, "testdata/shared_native_activity_check.mjs")
}
