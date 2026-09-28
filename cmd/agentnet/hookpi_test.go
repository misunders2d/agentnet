package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// The Pi extension is written to Pi's extensions directory only as
// AgentNet's own file: a foreign file there is never replaced or removed,
// and no backup or temporary file is left where Pi would load it.
func TestPiHooksInstallRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hooks are not supported on Windows")
	}
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", dir)
	home := filepath.Join(t.TempDir(), "agent home")
	ext := filepath.Join(dir, "extensions", "agentnet.ts")

	if err := runPiHooks(home, "install", nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(ext)
	if err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	absHome, _ := filepath.Abs(home)
	for _, want := range []string{"// agentnet-pi-extension v1", `const AGENTNET: string = ` + strconv.Quote(self), `const HOME: string = ` + strconv.Quote(absHome)} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("extension lacks %q", want)
		}
	}
	if bytes.Contains(data, []byte("__AGENTNET_")) {
		t.Fatal("placeholder left in the extension")
	}
	if err := runPiHooks(home, "install", nil); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Dir(ext))
	if len(entries) != 1 {
		t.Fatalf("extensions directory holds %d entries; want only agentnet.ts", len(entries))
	}
	if err := runPiHooks(home, "remove", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ext); !os.IsNotExist(err) {
		t.Fatal("not removed")
	}
	if err := runPiHooks(home, "remove", nil); err != nil {
		t.Fatalf("second remove: %v", err)
	}

	// Someone else's agentnet.ts is left alone.
	os.WriteFile(ext, []byte("// my own agentnet helper\n"), 0o644)
	for _, action := range []string{"install", "remove"} {
		if err := runPiHooks(home, action, nil); err == nil || !strings.Contains(err.Error(), "not written by AgentNet") {
			t.Fatalf("%s over a foreign file: %v", action, err)
		}
	}
	if data, _ := os.ReadFile(ext); string(data) != "// my own agentnet helper\n" {
		t.Fatal("foreign file changed")
	}
	if err := runPiHooks(home, "bogus", nil); err == nil {
		t.Fatal("unknown action accepted")
	}
}

// Without an enrolled agent the Pi hook prints nothing and creates nothing,
// for every event including Ack.
func TestHookPiWithoutAgentIsSilent(t *testing.T) {
	home := filepath.Join(t.TempDir(), "missing")
	for _, ev := range []string{"SessionStart", "UserPromptSubmit", "Idle", "Stop", "Ack", "PostToolUse", "Bogus"} {
		var out bytes.Buffer
		in := `{"session_id":"S","hook_event_name":"` + ev + `","pos":3,"has_pos":true}`
		if err := runHook(home, []string{"pi"}, strings.NewReader(in), &out); err != nil || out.Len() != 0 {
			t.Fatalf("%s: %v %q", ev, err, out.String())
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("the hook created the home")
	}
	t.Setenv("AGENTNET_BACKGROUND", "1")
	var out bytes.Buffer
	if err := runHook(home, []string{"pi"}, strings.NewReader(`{"session_id":"S","hook_event_name":"Idle"}`), &out); err != nil || out.Len() != 0 {
		t.Fatal("background session not silent")
	}
}

// piStub stands in for agentnet: it logs each call's event and answers
// every non-Ack event with one arrival, slowly, so overlapping calls would
// show in the log.
const piStub = `#!/bin/sh
in=$(cat)
printf '%s\n' "$in" >> "$STUB_LOG"
case "$in" in *'"Ack"'*) exit 0 ;; esac
sleep 0.2
printf '%s\n' '{"text":"AgentNet (me): new messages arrived since this session last checked:\n- m1 message from bob/desk","ack":{"pos":7,"has_pos":true,"release":""}}'
`

// piHarness loads the extension in Node with a stand-in for Pi's extension
// API, drives Pi's events, and prints what the extension did.
const piHarness = `
import * as fs from "node:fs";
const handlers = {};
const calls = [];
const pi = {
  on: (ev, fn) => { handlers[ev] = fn; },
  sendMessage: (m, o) => calls.push({ sendMessage: { display: m.display, customType: m.customType, triggerTurn: o.triggerTurn, deliverAs: o.deliverAs } }),
};
const mod = await import(process.argv[2]);
mod.default(pi);
const out = (x) => console.log(JSON.stringify(x));
if (!handlers.session_start) { out({ registered: false }); process.exit(0); }
let idle = false;
const ctx = { hasUI: true, isIdle: () => idle, sessionManager: { getSessionId: () => "S1" },
  ui: { notify: (t, k) => calls.push({ notify: t }) } };
await handlers.session_start({}, ctx);
// A prompt and an idle arrival at the same time.
idle = true;
const prompt = handlers.before_agent_start({});
fs.writeFileSync(process.argv[3] + "/agent.db-wal", "x");
out({ prompt: await prompt });
await new Promise((r) => setTimeout(r, 1500));
out({ settle1: await handlers.agent_before_settle({}) });
out({ settle2: await handlers.agent_before_settle({}) });
await handlers.session_shutdown({});
fs.writeFileSync(process.argv[3] + "/agent.db-wal", "y");
await new Promise((r) => setTimeout(r, 900));
out({ calls });
`

// The extension, run by Node against a stand-in Pi API: it asks at session
// start, prompt, idle and end of run; acknowledges only after handing text
// over; never overlaps two calls; continues a run once; stops watching at
// shutdown; and stays silent in AgentNet's background sessions. This checks
// the extension's own logic, not that Pi itself loads it.
func TestPiExtensionWithStandInAPI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stand-in agentnet is a shell script")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if out, err := exec.Command(node, "-e", "process.exit(Number(process.versions.node.split('.')[0]) >= 23 ? 0 : 1)").CombinedOutput(); err != nil {
		t.Skipf("node too old to load TypeScript directly: %s", out)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	os.MkdirAll(home, 0o700)
	stub := filepath.Join(dir, "agentnet")
	os.WriteFile(stub, []byte(piStub), 0o700)
	log := filepath.Join(dir, "calls.log")
	bin, _ := json.Marshal(stub)
	h, _ := json.Marshal(home)
	ext := bytes.Replace(piExtension, []byte(`"__AGENTNET_BIN__"`), bin, 1)
	ext = bytes.Replace(ext, []byte(`"__AGENTNET_HOME__"`), h, 1)
	os.WriteFile(filepath.Join(dir, "agentnet.ts"), ext, 0o600)
	os.WriteFile(filepath.Join(dir, "harness.mjs"), []byte(piHarness), 0o600)

	run := func(background string) []map[string]json.RawMessage {
		t.Helper()
		cmd := exec.Command(node, filepath.Join(dir, "harness.mjs"), filepath.Join(dir, "agentnet.ts"), home)
		cmd.Env = append(os.Environ(), "STUB_LOG="+log, "AGENTNET_BACKGROUND="+background)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, out)
		}
		var lines []map[string]json.RawMessage
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			var m map[string]json.RawMessage
			if json.Unmarshal([]byte(l), &m) == nil {
				lines = append(lines, m)
			}
		}
		return lines
	}

	lines := run("")
	got := map[string]string{}
	for _, l := range lines {
		for k, v := range l {
			got[k] = string(v)
		}
	}
	if !strings.Contains(got["prompt"], `"customType":"agentnet"`) || !strings.Contains(got["prompt"], "m1 message from bob/desk") {
		t.Fatalf("prompt context: %s", got["prompt"])
	}
	if !strings.Contains(got["settle1"], `"continue":true`) || !strings.Contains(got["settle1"], `"type":"custom_message"`) {
		t.Fatalf("end of run: %s", got["settle1"])
	}
	for _, want := range []string{`"notify":"AgentNet: 1 new"`, `"triggerTurn":false`, `"deliverAs":"nextTurn"`} {
		if !strings.Contains(got["calls"], want) {
			t.Fatalf("idle notice lacks %s: %s", want, got["calls"])
		}
	}

	data, _ := os.ReadFile(log)
	var events []string
	var stopActive []bool
	for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var in struct {
			Event string `json:"hook_event_name"`
			Stop  bool   `json:"stop_hook_active"`
			Pos   int64  `json:"pos"`
		}
		json.Unmarshal([]byte(l), &in)
		events = append(events, in.Event)
		if in.Event == "Stop" {
			stopActive = append(stopActive, in.Stop)
		}
		if in.Event == "Ack" && in.Pos != 7 {
			t.Fatalf("ack position %d", in.Pos)
		}
	}
	// Every shown notice is acknowledged right after it, never interleaved.
	for i, ev := range events {
		if ev != "Ack" && (i+1 >= len(events) || events[i+1] != "Ack") && !(ev == "Stop" && len(stopActive) > 1 && i == len(events)-1) {
			t.Fatalf("calls overlapped or a shown notice was not acknowledged: %v", events)
		}
	}
	want := []string{"SessionStart", "UserPromptSubmit", "Idle", "Stop"}
	var asked []string
	for _, ev := range events {
		if ev != "Ack" {
			asked = append(asked, ev)
		}
	}
	if strings.Join(asked[:4], ",") != strings.Join(want, ",") || len(asked) != 5 || asked[4] != "Stop" {
		t.Fatalf("events %v", events)
	}
	if len(stopActive) != 2 || stopActive[0] || !stopActive[1] {
		t.Fatalf("end-of-run continuation not guarded: %v", stopActive)
	}

	os.Remove(log)
	lines = run("1")
	if len(lines) != 1 || string(lines[0]["registered"]) != "false" {
		t.Fatalf("background session registered handlers: %v", lines)
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("background session called agentnet")
	}
}
