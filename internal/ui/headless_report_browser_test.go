package ui

import (
	"os/exec"
	"strings"
	"testing"
)

func TestBrowserHeadlessReportDismissal(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.Command(node, "testdata/headless_report_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PASS local report dismissal") {
		t.Fatalf("%v\n%s", err, out)
	}
}
