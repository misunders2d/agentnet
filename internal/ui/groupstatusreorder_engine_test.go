//go:build linux

package ui

import "testing"

func TestBrowserGroupStatusReorderedBeforeOriginal(t *testing.T) {
	testBrowserGroupCarrierEngine(t, "status-reorder")
}

func TestBrowserGroupStatusReorderedIndexedDB(t *testing.T) {
	testBrowserGroupCarrierIndexedDB(t, false, false, false, false, false, true)
}
