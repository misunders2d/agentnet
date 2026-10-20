package ui

import (
	"os/exec"
	"testing"
)

func TestBrowserDirectTopics(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/directtopics_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

func TestBrowserDirectTopicsRealIndexedDB(t *testing.T) {
	testOwnDeviceModuleIndexedDB(t, "directtopics_engine_check.mjs", "directTopicsResult")
}
