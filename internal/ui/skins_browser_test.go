package ui

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// demoPage serves the demo fixture's page and API on a loopback test
// server, as `agentnet ui --demo` does, with skins installed from dir.
func demoPage(t *testing.T, skins string) string {
	t.Helper()
	ts := httptest.NewUnstartedServer(nil)
	ts.Config.Handler = New(NewFixture(time.Now), ts.Listener.Addr().String(), testToken, skins).Handler()
	ts.Start()
	t.Cleanup(ts.Close)
	return ts.URL + "/?t=" + testToken
}

func browserCheck(t *testing.T, script string, args ...string) string {
	t.Helper()
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("opt-in: set AGENTNET_PLAYWRIGHT to an installed playwright-core (AGENTNET_CHROMIUM or /usr/bin/chromium)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, append([]string{script}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

// Comic and the Notebook example are standalone packages: mounted from
// copied package bytes at an unrelated path with only the public host,
// neither reads a private page global, talks to the API or event stream
// itself, loads an undeclared file or writes outside its root, and
// mount/unmount A/B/A leaves nothing behind.
func TestSkinPackagesContractOnly(t *testing.T) {
	out := browserCheck(t, "testdata/skin_contract_check.cjs", demoPage(t, ""),
		filepath.Join("static", "skins", "comic"), filepath.Join("..", "..", "examples", "skins", "notebook"))
	if !strings.Contains(out, "skin contract check PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Logf("%s", out)
}

// The production host with the real catalog: Comic mounted as a package
// (shadow root, fonts, popups inside it), Settings → Appearance → Skin,
// an installed skin behind its trust step with the host's switcher, the
// one step back to Comic, saved choices, and notification routing.
func TestSkinsRendered(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "notebook")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"skin.json", "entry.mjs", "style.css"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "examples", "skins", "notebook", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	out := browserCheck(t, "testdata/skins_browser_check.cjs", demoPage(t, home), filepath.Join("..", "..", "examples", "skins", "notebook"))
	if !strings.Contains(out, "skins browser check PASS") {
		t.Fatalf("no pass line:\n%s", out)
	}
	t.Logf("%s", out)
}

