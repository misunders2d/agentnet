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

// The relay's device page with each built-in skin, its server refusing
// this page's build and serving a newer one: it never reloads over a typed
// draft and an attached file (it says why and keeps both); sent, they wait
// in this browser's outbox, and the page then reloads by itself
// (testdata/update_reload_drafts_rendered.cjs). Opt-in: an installed
// Playwright (AGENTNET_PLAYWRIGHT) and Chromium.
func TestUpdateReloadKeepsDraftsRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	out, err := exec.CommandContext(t.Context(), "node", "testdata/update_reload_drafts_rendered.cjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "update reload keeps drafts PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// A browser page older than its server requires or serves is marked
// outdated once, asks its server only what it still takes from a refused
// device, and has the skin reload it (overview version) at most once per
// build and target (testdata/update_required_engine_check.mjs).
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
