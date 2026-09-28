package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// piStub stands in for agentnet. It logs each call, and per the mode file
// answers every non-Ack event with one arrival at position 7, hangs, writes
// far too much, or answers slowly (after marking the call in flight). A
// caller that asks to skip past position 7 (the earlier "after") gets the
// newer arrival at position 8.
const piStub = `#!/bin/sh
d="$STUB_DIR"
in=$(cat)
printf '%s\n' "$in" >> "$d/calls.log"
case "$in" in *'"Ack"'*) exit 0 ;; esac
case "$(cat "$d/mode" 2>/dev/null)" in
hang) echo $$ > "$d/hung.pid"; exec sleep 60 ;;
huge) head -c 200000 /dev/zero | tr '\0' x; exit 0 ;;
slow) : > "$d/inflight"; sleep 0.3 ;;
esac
pos=7
case "$in" in *'"after":'[1-9]*) pos=8 ;; esac
printf '%s%s%s\n' '{"text":"AgentNet (me): new messages arrived since this session last checked:\n- m1 message from bob/desk","ack":{"pos":' "$pos" ',"has_pos":true,"release":""}}'
`

// piHarness loads the extension in Node with a stand-in for Pi's extension
// API and drives Pi's events step by step, printing what happened.
const piHarness = `
import * as fs from "node:fs";
const [ext, home, dir] = process.argv.slice(2);
const h = {};
const sent = [];
const notes = [];
const pi = { on: (ev, fn) => { h[ev] = fn; }, sendMessage: (m, o) => sent.push({ details: m.details, options: o }) };
(await import(ext)).default(pi);
const out = (x) => console.log(JSON.stringify(x));
if (!h.session_start) { out({ registered: false }); process.exit(0); }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const mode = (m) => fs.writeFileSync(dir + "/mode", m);
const log = () => fs.readFileSync(dir + "/calls.log", "utf8").split("\n").filter((l) => l);
const acks = () => log().filter((l) => l.includes('"Ack"')).length;
const reads = () => log().filter((l) => !l.includes('"Ack"')).length;
let idle = true, branch = []; // idle when the session starts
const ctx = { hasUI: true, isIdle: () => idle, ui: { notify: (t) => notes.push(t) },
  sessionManager: { getSessionId: () => "S1", getBranch: () => branch } };
mode("ok");
await h.session_start({}, ctx);
out({ step: "start", acks: acks(), sent: sent.length, triggerTurn: sent[0]?.options?.triggerTurn });
idle = false;
const r1 = await h.before_agent_start({});
await sleep(100);
out({ step: "prompt", message: !!r1?.message, acks: acks() });
let n = reads();
const r2 = await h.before_agent_start({});
out({ step: "prompt-pending", message: !!r2?.message, reads: reads() - n });
// Still pending (position 7): no check runs, so no newer notice (position 8)
// can be admitted and acknowledged over it.
n = reads();
const s0 = await h.agent_before_settle({});
branch = s0?.entries ? [{ type: "custom_message", customType: "agentnet", details: s0.entries[0].details }] : [];
await h.turn_start({}, ctx);
await sleep(300);
out({ step: "settle-pending", entries: !!s0?.entries, reads: reads() - n, acks: acks(), pos8: log().some((l) => l.includes('"pos":8')) });
branch = [];
await h.message_end({ message: { role: "custom", customType: "agentnet", details: r1.message.details } });
await sleep(300);
out({ step: "admitted", acks: acks() });
idle = true;
fs.writeFileSync(home + "/agent.db-wal", "x");
await sleep(1200);
out({ step: "idle", notes, acks: acks(), idleSent: sent.length });
// A run starts while the idle check is being answered: nothing is shown or
// acknowledged (Pi would only queue the message until the turn ends).
mode("slow");
let sent0 = sent.length;
fs.writeFileSync(home + "/agent.db-wal", "z");
for (let i = 0; i < 150 && !fs.existsSync(dir + "/inflight"); i++) await sleep(20);
idle = false;
await sleep(800);
out({ step: "delayed-idle", inflight: fs.existsSync(dir + "/inflight"), notes: notes.length, acks: acks(), sent: sent.length - sent0 });
mode("ok");
const s1 = await h.agent_before_settle({});
await sleep(100);
out({ step: "settle", continue: s1?.continue, acks: acks() });
branch = [{ type: "custom_message", customType: "agentnet", details: s1.entries[0].details }];
await h.turn_start({}, ctx);
await sleep(300);
out({ step: "settle-admitted", acks: acks() });
const r3 = await h.before_agent_start({});
// The run settles without it: dropped, and what was deferred during the run
// is checked once Pi is idle.
idle = true;
n = reads();
let notes0 = notes.length;
await h.agent_settled({}, { sessionManager: { getBranch: () => [] } });
await sleep(900);
out({ step: "dropped", first: !!r3?.message, recheck: reads() - n, recheckNotes: notes.length - notes0 });
idle = false;
const r4 = await h.before_agent_start({});
out({ step: "reoffered", again: !!r4?.message });
await h.agent_settled({}, { sessionManager: { getBranch: () => [] } });
mode("hang");
let t0 = Date.now();
const r5 = await h.before_agent_start({});
out({ step: "hang", ms: Date.now() - t0, message: !!r5 });
mode("huge");
const r6 = await h.before_agent_start({});
mode("ok");
const r7 = await h.before_agent_start({});
out({ step: "recover", huge: !!r6, message: !!r7 });
await h.agent_settled({}, { sessionManager: { getBranch: () => [] } });
mode("hang");
fs.rmSync(dir + "/hung.pid", { force: true });
const p = h.before_agent_start({});
await sleep(150);
await h.session_shutdown({});
await sleep(50); // well before the hook timeout: only shutdown can have stopped it
let alive = true;
try { process.kill(Number(fs.readFileSync(dir + "/hung.pid", "utf8")), 0); } catch { alive = false; }
out({ step: "shutdown", hungAlive: alive });
await p;
fs.writeFileSync(home + "/agent.db-wal", "y");
await sleep(900);
out({ step: "after-shutdown", notes: notes.length });
`

// The extension, run by Node against a stand-in Pi API (this checks its own
// logic, not that Pi itself loads it): acknowledgement only once the notice
// is in the session (at once for idle and start, at message_end for a
// prompt, once the entry is in the branch for the end of a run); while one
// is pending no other check runs, so a newer notice is never acknowledged
// over it, and what was deferred is checked once the run has settled;
// unconfirmed notices are offered again after the run; an idle notice whose
// answer arrives after a run has started is neither shown nor acknowledged;
// a hung or oversized hook call is cut off and later calls work; shutdown
// stops a running call and the watcher; background sessions do nothing.
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
	bin, _ := json.Marshal(stub)
	h, _ := json.Marshal(home)
	ext := bytes.Replace(piExtension, []byte(`"__AGENTNET_BIN__"`), bin, 1)
	ext = bytes.Replace(ext, []byte(`"__AGENTNET_HOME__"`), h, 1)
	ext = bytes.Replace(ext, []byte("const HOOK_TIMEOUT_MS = 10000;"), []byte("const HOOK_TIMEOUT_MS = 1000;"), 1)
	os.WriteFile(filepath.Join(dir, "agentnet.ts"), ext, 0o600)
	os.WriteFile(filepath.Join(dir, "harness.mjs"), []byte(piHarness), 0o600)

	run := func(background string) map[string]map[string]any {
		t.Helper()
		os.Remove(filepath.Join(dir, "calls.log"))
		os.WriteFile(filepath.Join(dir, "calls.log"), nil, 0o600)
		cmd := exec.Command(node, filepath.Join(dir, "harness.mjs"), filepath.Join(dir, "agentnet.ts"), home, dir)
		cmd.Env = append(os.Environ(), "STUB_DIR="+dir, "AGENTNET_BACKGROUND="+background)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("node: %v\n%s", err, out)
		}
		steps := map[string]map[string]any{}
		for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			var m map[string]any
			if json.Unmarshal([]byte(l), &m) == nil {
				name, _ := m["step"].(string)
				if name == "" {
					name = "registered"
				}
				steps[name] = m
			}
		}
		return steps
	}

	s := run("")
	check := func(step, key string, want any) {
		t.Helper()
		if got := s[step][key]; fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s.%s = %v, want %v (all: %v)", step, key, got, want, s[step])
		}
	}
	check("start", "acks", 1) // in the session at once
	check("start", "triggerTurn", false)
	check("prompt", "message", true)
	check("prompt", "acks", 1) // not yet: Pi adds it after the handler returns
	check("prompt-pending", "message", false)
	check("prompt-pending", "reads", 0) // deferred while one is pending
	check("settle-pending", "reads", 0)
	check("settle-pending", "entries", false)
	check("settle-pending", "acks", 1)
	check("settle-pending", "pos8", false) // never acknowledged over the pending 7
	check("admitted", "acks", 2)
	check("idle", "acks", 3)
	check("idle", "notes", "[AgentNet: 1 new]")
	check("delayed-idle", "inflight", true)
	check("delayed-idle", "notes", 1) // no notice once a run has started
	check("delayed-idle", "sent", 0)
	check("delayed-idle", "acks", 3) // and nothing acknowledged
	check("settle", "continue", true)
	check("settle", "acks", 3)
	check("settle-admitted", "acks", 4)
	check("dropped", "first", true)
	check("dropped", "recheck", 1) // the deferred check, once idle
	check("dropped", "recheckNotes", 1)
	check("reoffered", "again", true)
	check("hang", "message", false)
	if ms, _ := s["hang"]["ms"].(float64); ms > 3000 {
		t.Errorf("a hung hook call held the prompt for %v ms", ms)
	}
	check("recover", "huge", false)
	check("recover", "message", true)
	check("shutdown", "hungAlive", false)
	check("after-shutdown", "notes", 2)

	s = run("1")
	if v, ok := s["registered"]["registered"]; !ok || v != false {
		t.Fatalf("background session registered handlers: %v", s)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "calls.log")); len(data) != 0 {
		t.Fatal("background session called agentnet")
	}
}
