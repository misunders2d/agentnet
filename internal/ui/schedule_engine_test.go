package ui

import "testing"

// Real signed ordinary routes and source-driven storage/timer races. No model,
// live infrastructure or rendered browser is used by this focused check.
func TestBrowserScheduledSendCore(t *testing.T) {
	runNodeCheck(t, "testdata/schedule_engine_check.mjs")
}
