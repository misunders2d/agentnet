package ui

import (
	"strings"
	"testing"
)

func TestCompactHostControlsRendered(t *testing.T) {
	privateScreenshots(t)
	out := browserCheck(t, "testdata/compact_host_rendered.cjs")
	if !strings.Contains(out, "Compact host controls PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Log(out)
}
