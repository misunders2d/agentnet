package ui

import "testing"

func TestMobileUnreadVisibility(t *testing.T) {
	runNodeCheck(t, "testdata/mobile_visibility_check.mjs")
}
