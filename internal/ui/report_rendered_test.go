package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestReportRendered: Report sends the reporter's own message to each admin
// person, with immediate pending feedback, a truthful failure and a retry
// that repeats the same reports (Google Play user-generated content policy).
func TestReportRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered report checks")
	}
	output, err := exec.CommandContext(t.Context(), "node", "testdata/report_rendered.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(output), "Report PASS") {
		t.Fatalf("rendered report: %v\n%s", err, output)
	}
	t.Log(string(output))
}
