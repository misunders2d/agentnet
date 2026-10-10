package ui

import "testing"

// The browser receive path keeps a failing envelope aside instead of ending
// the stream, answers heartbeats while catching up, resumes a stream when the
// page is shown again, and reads authority one conversation at a time
// (testdata/receive_resilience_check.mjs, store_conv_index_check.mjs).
func TestBrowserReceiveResilience(t *testing.T) {
	runNodeCheck(t, "testdata/receive_resilience_check.mjs")
}

func TestBrowserReceiveResilienceRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "receive_resilience_check.mjs", "receiveResilienceResult")
}

func TestBrowserConvIndex(t *testing.T) {
	runNodeCheck(t, "testdata/store_conv_index_check.mjs")
}

// The real database upgrade: a v5 database with rows gains the conv index.
func TestBrowserConvIndexRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "store_conv_index_check.mjs", "convIndexResult")
}

// Catch-up never echoes history to the exact device that forwarded it, and a
// sibling copy that cannot be made never fails its original's admission.
func TestBrowserHistoryForward(t *testing.T) {
	runNodeCheck(t, "testdata/history_forward_check.mjs")
}
