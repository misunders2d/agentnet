package ui

import (
	"os/exec"
	"testing"
)

func TestBrowserRequestContinuation(t *testing.T) {
	if _, e := exec.LookPath("node"); e != nil {
		t.Skip("node unavailable")
	}
	out, e := exec.CommandContext(t.Context(), "node", "testdata/continuation_check.mjs").CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	t.Log(string(out))
}
