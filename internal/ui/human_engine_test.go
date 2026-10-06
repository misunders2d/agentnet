package ui

import (
	"os/exec"
	"testing"
)

// Real browser crypto and local isolated transport; no model or live service.
// Keep human-engine lifecycle checks in the normal Go suite, like other engines.
func TestBrowserHumanEngineLifecycle(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/human_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("human engine lifecycle: %v\n%s", err, out)
	}
	t.Log(string(out))
}
