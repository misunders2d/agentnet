package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// P24's controls over a public, captured host with synthetic authoritative
// replies (no Google or live membership changes). Checks daemon and browser
// at desktop/light and phone/dark; keeps a failing invitation for retry.
func TestComicSetupParityRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	f := NewFixture(time.Now)
	s := New(f, "127.0.0.1:0", testToken)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	cmd := exec.CommandContext(t.Context(), node, "testdata/comic_setup_parity_check.cjs")
	cmd.Env = append(os.Environ(), "PARITY_URL="+ts.URL+"/?t="+testToken)
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "comic setup parity check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
