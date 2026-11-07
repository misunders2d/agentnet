package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// Cancel of a queued send in the real daemon page and built Comic bundle:
// exact restoration, a restoration storage refused recovered after a reload,
// and one active tab. Synthetic fixture; only queued-send endpoints are stubbed.
func TestCancelRestoreRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in installed Playwright")
	}
	for _, width := range []string{"1440", "390"} {
		f := NewFixture(time.Now)
		if _, _, err := f.CreatePerson("Fixture Me"); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewUnstartedServer(nil)
		ts.Config.Handler = New(f, ts.Listener.Addr().String(), testToken).Handler()
		ts.Start()
		cmd := exec.Command("node", "testdata/cancel_restore_rendered_check.cjs", ts.URL+"/?t="+testToken)
		cmd.Env = append(os.Environ(), "CANCEL_WIDTH="+width)
		out, err := cmd.CombinedOutput()
		ts.Close()
		if err != nil || !strings.Contains(string(out), "Cancel restore rendered PASS") {
			t.Fatalf("width %s: %v\n%s", width, err, out)
		}
		t.Logf("%s", out)
	}
}
