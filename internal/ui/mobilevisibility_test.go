package ui

import "testing"

func TestMobileUnreadVisibility(t *testing.T) {
	runNodeCheck(t, "testdata/mobile_visibility_check.mjs")
}

func TestComicNotificationLanding(t *testing.T) {
	runNodeCheck(t, "testdata/notification_landing_check.mjs")
}
