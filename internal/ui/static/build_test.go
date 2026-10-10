package static

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func relayGet(t *testing.T, h http.Handler, path string) (body, etag string) {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s: %d", path, w.Code)
	}
	return w.Body.String(), w.Header().Get("ETag")
}

// A relay writes its own version into the engine it serves, and changes
// nothing else; the engine from anywhere else says none.
func TestRelayBuildStampsEngine(t *testing.T) {
	relay := RelayBuild("v1.2.3")
	stamped, stampedTag := relayGet(t, relay, "/assets/engine.mjs")
	plain, plainTag := relayGet(t, Relay(), "/assets/engine.mjs")
	if !strings.Contains(plain, buildStamp) {
		t.Fatalf("the engine has no %q", buildStamp)
	}
	if strings.Replace(stamped, `export const BUILD = "v1.2.3";`, buildStamp, 1) != plain {
		t.Fatal("the stamped engine differs in more than its build")
	}
	if stampedTag == plainTag {
		t.Fatal("the stamped engine kept the unstamped ETag")
	}
	for p := range relayFiles {
		if p == "/assets/engine.mjs" {
			continue
		}
		if a, _ := relayGet(t, relay, p); a != relayContent()[p][0] {
			t.Fatalf("%s changed with the build", p)
		}
	}
}

// buildCheck drives the served engine in node with a fake relay: every
// signed request, the push stream included, says the engine's build, an
// unsigned one does not, and a relay of another origin gets it only once it
// lists update1 (an older relay's CORS check refuses the header).
const buildCheck = `import { Engine, BUILD, memoryStore } from "./assets/engine.mjs";
import * as wire from "./assets/wire.mjs";
let features = [];
const sent = [];
const fetch = async (url, opts = {}) => {
  const u = new URL(url);
  sent.push((opts.method || "GET") + " " + u.pathname + " " + ((opts.headers || {})["Agentnet-Version"] || "-"));
  if (u.pathname === "/v1/stream") return new Response("{}", { status: 503 });
  const body = u.pathname === "/v1/version" ? { version: "v1.2.3", protocol: 1, features } : {};
  return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
};
const e = new Engine({ store: memoryStore(), base: "https://relay.example", fetch });
e.keys = await wire.newKeys();
e.address = "eve/phone";
await e.call("GET", "/v1/agents");
await e.call("GET", "/v1/version", undefined, { signed: false });
await e.callBytes("PUT", "/v1/blobs/b", new Uint8Array([1]));
await e.getBytes("/v1/blobs/b/data");
await e.streamOnce();
globalThis.location = { origin: "https://page.example" };
sent.push("older relay of another origin");
e.featureList = null; features = ["caps"];
await e.call("GET", "/v1/agents");
sent.push("relay of another origin with update1");
e.featureList = null; features = ["caps", "update1"];
await e.call("GET", "/v1/agents");
await e.streamOnce();
console.log(JSON.stringify({ build: BUILD, sent }));
process.exit(0);
`

func TestServedEngineSendsBuild(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	dir := t.TempDir()
	relay := RelayBuild("v1.2.3")
	for p := range relayFiles {
		if !strings.HasPrefix(p, "/assets/") {
			continue
		}
		body, _ := relayGet(t, relay, p)
		path := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "check.mjs")
	if err := os.WriteFile(script, []byte(buildCheck), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, script).Output()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var got struct {
		Build string   `json:"build"`
		Sent  []string `json:"sent"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	want := []string{
		"GET /v1/agents v1.2.3",
		"GET /v1/version -",
		"PUT /v1/blobs/b v1.2.3",
		"GET /v1/blobs/b/data v1.2.3",
		"GET /v1/stream v1.2.3",
		"older relay of another origin",
		"GET /v1/version -",
		"GET /v1/agents -",
		"relay of another origin with update1",
		"GET /v1/version -",
		"GET /v1/agents v1.2.3",
		"GET /v1/version -",
		"GET /v1/stream v1.2.3",
	}
	if got.Build != "v1.2.3" || strings.Join(got.Sent, "\n") != strings.Join(want, "\n") {
		t.Fatalf("build %q, requests:\n%s\nwant:\n%s", got.Build, strings.Join(got.Sent, "\n"), strings.Join(want, "\n"))
	}
}
