package ui

import (
	"os/exec"
	"testing"
)

func TestRawLinkText(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	if out, err := exec.CommandContext(t.Context(), node, "testdata/link_text_check.mjs").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
