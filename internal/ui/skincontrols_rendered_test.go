package ui

import (
	"strings"
	"testing"
)

// Uses the embedded Comic by default. The collection and a private source
// preview can be supplied for focused pre-build all-skin verification.
func TestSkinControlsQuotesAndCollapsedPanelRendered(t *testing.T) {
	privateScreenshots(t)
	out := browserCheck(t, "testdata/skin_controls_rendered.cjs")
	if !strings.Contains(out, "Actual skin controls PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Log(out)
}
