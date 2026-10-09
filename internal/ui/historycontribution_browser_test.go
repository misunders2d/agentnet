package ui

import (
	"os/exec"
	"testing"
)

func TestBrowserHistoryContribution(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/historycontribution_engine_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
}
