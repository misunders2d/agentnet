package ui

import (
	"strings"
	"testing"
)

func TestComicAttachedUpdaterUnavailable(t *testing.T) {
	out := browserCheck(t, "testdata/appcontrols_browser_check.cjs", demoPage(t, ""))
	if !strings.Contains(out, "attached updater check PASS") {
		t.Fatalf("no pass line: %s", out)
	}
}
