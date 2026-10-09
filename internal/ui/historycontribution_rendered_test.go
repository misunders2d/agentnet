package ui

import (
	"os"
	"strings"
	"testing"
)

// The production Comic skin over an inert public Host: no Hub or model calls.
func TestHistoryContributionComicRendered(t *testing.T) {
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("P11_HISTORY_IMPORT_ONLY", "1")
	if os.Getenv("AGENTNET_SCREENSHOTS") == "" {
		t.Setenv("AGENTNET_SCREENSHOTS", t.TempDir())
	}
	out := browserCheck(t, "testdata/chattopics_rendered.cjs")
	if strings.Count(out, "HISTORY IMPORT PASS ") != 4 {
		t.Fatalf("history contribution combinations missing:\n%s", out)
	}
	t.Log(out)
}
