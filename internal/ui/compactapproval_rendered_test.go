package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Synthetic projections only: no real approval, cancellation or agent runs.
func TestCompactProposalApprovalRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	f := &needsYouFixture{Fixture: NewFixture(time.Now)}
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
	ts.Start()
	defer ts.Close()
	cmd := exec.Command("node", "testdata/compact_approval_rendered_check.cjs", ts.URL+"/?t="+testToken)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Compact approval rendered PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
