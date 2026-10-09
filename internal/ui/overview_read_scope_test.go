package ui

import (
	"os/exec"
	"testing"
)

func TestBrowserOverviewReadsOnlyDisplayRecords(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/overview_read_scope_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserOverviewReadScopeRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "overview_read_scope_check.mjs", "overviewReadScopeResult")
}
