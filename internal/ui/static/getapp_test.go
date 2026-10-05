package static

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// No page is installable from a computer's browser: the AgentNet app is
// the one way to open AgentNet there (MEL-536), so neither the daemon's page
// nor the relay's links a manifest; landing.mjs adds it on phones.
func TestIndexHasNoManifestLink(t *testing.T) {
	for name, page := range map[string][]byte{"index.html": must(fs.ReadFile(Files, "index.html")), "device page": devicePage(), "setup page": SetupPage()} {
		if strings.Contains(string(page), "manifest") {
			t.Errorf("%s links a manifest", name)
		}
	}
	if !strings.Contains(string(SetupPage()), `<script type="module" src="/assets/setup.mjs">`) || strings.Contains(string(SetupPage()), "loader.js") {
		t.Fatal("the setup page does not start setup.mjs alone")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

// getapp.json is the one table of the app's installers: Go's Downloads
// and the page's downloads() read it the same way, and desktop/build.sh
// and the release workflow make files of exactly those names.
func TestGetAppTable(t *testing.T) {
	rel := Downloads("v1.2.3")
	latest := Downloads("v1.2.3-4-gabcdef0")
	if len(rel) != len(getApp().Platforms) || len(rel) < 5 {
		t.Fatalf("downloads %+v", rel)
	}
	ids := map[string]bool{}
	for i, d := range rel {
		ids[d.ID] = true
		if d.URL != Releases()+"/download/v1.2.3/"+d.Asset || latest[i].URL != Releases()+"/latest/download/"+d.Asset {
			t.Errorf("urls %s %s", d.URL, latest[i].URL)
		}
	}
	for _, id := range []string{"windows", "macos", "linux"} {
		if !ids[id] {
			t.Errorf("no %s installer", id)
		}
	}
	build, err := os.ReadFile(filepath.Join("..", "..", "..", "desktop", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range rel {
		if !strings.Contains(string(build), d.Asset) {
			t.Errorf("desktop/build.sh makes no %s", d.Asset)
		}
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	want, _ := json.Marshal(rel)
	wantLatest, _ := json.Marshal(latest)
	out, err := exec.Command(node, "--input-type=module", "-e", getAppCases, string(want), string(wantLatest)).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "getapp PASS") {
		t.Fatalf("%v\n%s", err, out)
	}
}

const getAppCases = `
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
const g = await import(pathToFileURL("getapp.mjs").href);
const table = JSON.parse(readFileSync("getapp.json", "utf8"));
const [want, wantLatest] = process.argv.slice(1).map((s) => JSON.parse(s));
assert.deepEqual(g.downloads(table, "v1.2.3"), want, "the page's downloads are Go's");
assert.deepEqual(g.downloads(table, "v1.2.3-4-gabcdef0"), wantLatest, "a development build offers the latest release");
const cases = [
  ["Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36", "", 0, "windows"],
  ["Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/141.0", "Windows", 0, "windows"],
  ["Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15", "", 0, "macos"],
  ["Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15", "", 5, "ipad"],
  ["Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Safari/537.36", "Linux", 0, "linux"],
  ["Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0 Mobile Safari/537.36", "Android", 5, "android"],
  ["Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1", "", 5, "iphone"],
  ["Mozilla/5.0 (iPad; CPU OS 17_0 like Mac OS X)", "", 5, "ipad"],
  ["Mozilla/5.0 (X11; CrOS x86_64 14541.0.0)", "Chrome OS", 0, ""],
  ["", "", 0, ""],
];
for (const [ua, platform, touch, want] of cases) assert.equal(g.detectPlatform(ua, platform, touch), want, ua);
assert.equal(g.isPhone("android") && g.isPhone("iphone") && g.isPhone("ipad"), true);
assert.equal(g.isPhone("linux") || g.isPhone(""), false);
assert.equal(g.forPlatform(want, "linux").asset, "AgentNet-linux-x86_64.AppImage", "Linux gets the AppImage first");
assert.equal(g.forPlatform(want, ""), null);
assert.equal(g.appLink("agentnet-invite-v1:x"), "agentnet://open#agentnet-invite-v1:x");
assert.equal(g.appLink(""), "agentnet://open");
console.log("getapp PASS");
`
