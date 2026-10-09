package ui

import "testing"

func TestBrowserHistoryContributionRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "historycontribution_engine_check.mjs", "historyContributionResult")
}
