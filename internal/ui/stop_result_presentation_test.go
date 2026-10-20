package ui

import (
	"os"
	"testing"
)

func TestStopResultPresentation(t *testing.T) {
	if _, err := os.Stat("web/node_modules/esbuild"); err != nil {
		t.Skip("installed pinned frontend compiler required")
	}
	runNodeCheck(t, "testdata/stop_result_presentation_check.cjs")
}

func TestStopTerminalResultRendered(t *testing.T) {
	t.Setenv("P11_SKINS", "comic")
	t.Setenv("AGENTNET_SCREENSHOTS", t.TempDir())
	t.Setenv("P11_STOP_ONLY", "1")
	t.Setenv("P11_STOP_RESULT_ONLY", "1")
	t.Log(browserCheck(t, "testdata/chattopics_rendered.cjs"))
}
