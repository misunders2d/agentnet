package ui

import (
	"os/exec"
	"testing"
)

func TestSendGroupBrowser(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	for _, script := range []string{"testdata/send_group_check.mjs", "testdata/send_group_display_check.mjs"} {
		if out, err := exec.CommandContext(t.Context(), "node", script).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v\n%s", script, err, out)
		}
	}
}
