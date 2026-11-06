package ui

import (
	"os/exec"
	"testing"
)

func TestCancelledPendingSendRestoresOwnedDraft(t *testing.T) {
	out, err := exec.CommandContext(t.Context(), "node", "testdata/cancel_send_draft_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
