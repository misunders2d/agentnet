package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// MEL-821: Office, built in, opens on a profile that never chose a skin,
// offline; saved choices (Comic too) are kept; a gallery Office moves to the
// built-in one with its theme; nothing is recorded for a profile that never
// chose.
func TestOfficeDefaultRendered(t *testing.T) {
	if testing.Short() {
		t.Skip("rendered")
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(NewFixture(time.Now), ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	t.Setenv("AGENTNET_TEST_FRESH_SKIN", "1") // the real first open (testdata/comic_preload.cjs)
	t.Setenv("AGENTNET_SCREENSHOTS", privateScreenshots(t))
	out := browserCheck(t, "testdata/office_default_rendered.cjs", ts.URL+"/?t="+testToken)
	if !strings.Contains(out, "office default rendered PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Logf("%s", out)
}
