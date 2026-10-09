//go:build linux

package ui

import "testing"

func TestBrowserGroupOutgoingControlHistory(t *testing.T) {
	testBrowserGroupCarrierEngine(t, "control-history")
}

func TestBrowserGroupOutgoingControlHistoryIndexedDB(t *testing.T) {
	testBrowserGroupCarrierIndexedDB(t, true)
}
