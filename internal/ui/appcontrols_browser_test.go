package ui

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestComicAttachedUpdaterUnavailable(t *testing.T) {
	out := browserCheck(t, "testdata/appcontrols_browser_check.cjs", demoPage(t, ""))
	if !strings.Contains(out, "attached updater check PASS") {
		t.Fatalf("no pass line: %s", out)
	}
}

func TestBundledAppReleaseCheckRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to installed Playwright")
	}
	cmd := exec.CommandContext(t.Context(), "node", "testdata/appcontrols_browser_check.cjs", demoPage(t, ""), "--release-check")
	cmd.Env = append(os.Environ(), "AGENTNET_SCREENSHOTS="+t.TempDir())
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "published release check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
