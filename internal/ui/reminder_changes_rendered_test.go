package ui

import (
	"os"
	"os/exec"
	"testing"
)

func TestReminderChangesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered reminder-change checks")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/reminder_changes_rendered.cjs")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("rendered reminder changes: %v\n%s", err, output)
	}
	t.Log(string(output))
}
