package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Synthetic execution states; no model, live permission, or job is changed.
func TestOwnRetryRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	for _, mode := range []string{"desktop", "phone"} {
		f := &needsYouFixture{Fixture: NewFixture(time.Now)}
		ts := httptest.NewUnstartedServer(nil)
		ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
		ts.Start()
		cmd := exec.Command("node", "testdata/own_retry_rendered_check.cjs", ts.URL+"/?t="+testToken)
		cmd.Env = append(os.Environ(), "RETRY_MODE="+mode)
		out, err := cmd.CombinedOutput()
		ts.Close()
		if err != nil || !strings.Contains(string(out), "Own retry rendered PASS") {
			t.Fatalf("%s: %v\n%s", mode, err, out)
		}
		t.Logf("%s", out)
	}
}
