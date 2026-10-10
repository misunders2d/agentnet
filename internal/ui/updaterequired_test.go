package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Comic's "Update AgentNet to vX to continue" banner, rendered
// (testdata/update_required_rendered_check.cjs). Opt-in: an installed
// Playwright (AGENTNET_PLAYWRIGHT) and Chromium.
func TestUpdateRequiredRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(NewFixture(time.Now), ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	out, err := exec.Command("node", "testdata/update_required_rendered_check.cjs", ts.URL+"/?t="+testToken).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Update required rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// A browser page older than its server requires or serves is marked
// outdated once, stops asking its server for anything but its stream, ping
// acks and the version probe, and is told to reload at most once per build
// and target (testdata/update_required_engine_check.mjs).
func TestBrowserUpdateRequired(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node unavailable")
	}
	out, err := exec.CommandContext(t.Context(), "node", "testdata/update_required_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
