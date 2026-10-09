//go:build linux

package ui

import "testing"

func TestBrowserNamedReceivePriorityBeforeGroupProof(t *testing.T) {
	testBrowserGroupCarrierEngine(t, "receive-priority")
}

func TestBrowserNamedReceivePriorityIndexedDB(t *testing.T) {
	testBrowserGroupCarrierIndexedDB(t, false, false, false, false, false, false, true)
}
