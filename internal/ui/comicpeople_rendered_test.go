package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestComicPeopleRendered checks the default messenger on the demo fixture
// at 1280x900 and 390x844 (MEL-529, MEL-525, MEL-524): the own phone is
// never "Your agent · on Pixel" (its thread is this computer's agent, asked
// from it, and what was typed there is yours, on the right); no Ask toward
// a device that runs no agent; another person's phone is that person, with
// a short key when only listed; the workspace coin and pill show the
// workspace's own name. Opt-in: needs an installed Playwright
// (AGENTNET_PLAYWRIGHT) and Chromium (AGENTNET_CHROMIUM or /usr/bin/chromium);
// AGENTNET_SCREENSHOTS keeps screenshots outside the repository.
func TestComicPeopleRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	f := NewFixture(time.Now)
	if _, _, err := f.CreatePerson("Sergey"); err != nil {
		t.Fatal(err)
	}
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(f, ts.Listener.Addr().String(), testToken)
	ts.Start()
	cmd := exec.Command(node, "testdata/comic_people_ui_check.cjs")
	cmd.Env = append(os.Environ(), "PEOPLE_URL="+ts.URL+"/?t="+testToken)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "comic people ui check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
