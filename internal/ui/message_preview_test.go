package ui

import (
	"os/exec"
	"testing"
)

func TestMessageReminderPreviewOnDemand(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for Comic message reminder model checks")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/message_preview_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
