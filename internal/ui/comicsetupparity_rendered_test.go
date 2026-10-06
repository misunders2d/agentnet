package ui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
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
	cmd := exec.CommandContext(t.Context(), node, "testdata/comic_setup_parity_check.cjs")
	// The existing demo helper binds the guard to the allocated listener,
	// rather than a requested port 0 which no browser request can match.
	cmd.Env = append(os.Environ(), "PARITY_URL="+demoPage(t, ""))
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "comic setup parity check PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// Check the setup fixture's actual HTTP boundary without Chromium. A host
// mismatch used to stop its first overview read before any UI could mount.
func TestComicSetupParityFixture(t *testing.T) {
	u, err := url.Parse(demoPage(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	u.Path, u.RawQuery = "/api/overview", ""
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("setup overview returned %s", r.Status)
	}
	var o Overview
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil || o.Role != RoleUnset || o.Me.Address == "" {
		t.Fatalf("setup fixture is not ready: role=%q address=%q error=%v", o.Role, o.Me.Address, err)
	}
}
