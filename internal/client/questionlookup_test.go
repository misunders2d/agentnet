package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// bindProgram makes the lookups bind to a stand-in program for the test.
func bindProgram(t *testing.T, path string) {
	t.Helper()
	old := executable
	executable = func() (string, error) { return path, nil }
	t.Cleanup(func() { executable = old })
}

// lookupProgram is an executable that prints its arguments, or fails when
// its first argument is "approvals".
func lookupProgram(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTNET_TEST_LOOKUP_PROGRAM", "1")
	return resolved
}

// Lookup hints identify the executable without injecting native permission rules.
func TestQuestionLookupClaudeExactRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	w := newWorld(t, "")
	exe := lookupProgram(t)
	bindProgram(t, exe)
	sent, err := w.alice.Send(tctx(t), w.bob.Address, "mine", "")
	if err != nil {
		t.Fatal(err)
	}
	foreign := protocol.NewID()
	j := job{ID: protocol.NewID(), From: w.bob.Address, Kind: envelope.KindQuestion,
		Body: "status of " + sent.ID + " and " + foreign + " and " + sent.ID + "; then rm -rf /"}
	setup := w.alice.questionSetup(j, "claude")
	if len(setup.args) != 0 {
		t.Fatalf("lookup injects native permission rules: %v", setup.args)
	}
	if !strings.Contains(setup.text, strconv.Quote(exe)) || !strings.Contains(setup.text, "`inbox --peek`") || strings.Contains(setup.text, foreign) {
		t.Fatalf("lookup hints: %q", setup.text)
	}
	if task := w.alice.questionSetup(job{Kind: envelope.KindTask, Body: sent.ID}, "claude"); task.args != nil || task.text != "" {
		t.Fatalf("task got lookups %+v", task)
	}
	if codex := w.alice.questionSetup(j, "codex"); codex.args != nil || !strings.Contains(codex.text, strconv.Quote(exe)) || !strings.Contains(codex.text, "`inbox --peek`") {
		t.Fatalf("codex %+v", codex)
	}
	// Space-containing executable paths remain quoted; unavailable programs fail.
	spaced := filepath.Join(t.TempDir(), "a b")
	os.MkdirAll(spaced, 0o700)
	odd := filepath.Join(spaced, "agentnet")
	os.WriteFile(odd, []byte("#!/bin/sh\n"), 0o700)
	bindProgram(t, odd)
	resolvedOdd, err := filepath.EvalSymlinks(odd)
	if err != nil {
		t.Fatal(err)
	}
	if got := w.alice.questionSetup(j, "claude"); got.args != nil || !strings.Contains(got.text, strconv.Quote(resolvedOdd)) {
		t.Fatalf("quoted path unavailable %+v", got)
	}
	bindProgram(t, filepath.Join(t.TempDir(), "missing"))
	for _, h := range []string{"claude", "codex", "pi"} {
		if got := w.alice.questionSetup(j, h); got.args != nil || got.text != lookupsUnavailable {
			t.Fatalf("%s without a program: %+v", h, got)
		}
	}
	r := &Responder{Harness: "claude", Dir: t.TempDir()}
	if p, err := w.alice.prompt(j, r); err != nil || !strings.Contains(p, lookupsUnavailable) || strings.Contains(p, "you may run exactly") {
		t.Fatalf("prompt claims lookups that were not configured (%v)", err)
	}
}

// The Pi extension registers one tool that runs only the bound program with
// fixed arguments and fails closed on anything else.
func TestQuestionLookupPiExtension(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi not installed")
	}
	pi, _ = filepath.EvalSymlinks(pi)
	piAI := ""
	for dir := filepath.Dir(pi); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if c := filepath.Join(dir, "node_modules", "@earendil-works", "pi-ai"); fileExists(c) {
			piAI = c
			break
		}
	}
	if piAI == "" {
		t.Skip("installed pi-ai not found beside pi")
	}
	w := newWorld(t, "")
	exe := lookupProgram(t)
	bindProgram(t, exe)
	setup := w.alice.questionSetup(job{Kind: envelope.KindQuestion}, "pi")
	args := setup.args
	if len(args) != 2 || args[0] != "--extension" || !strings.Contains(setup.text, "agentnet_lookup") {
		t.Fatalf("pi setup %+v", setup)
	}
	// An extension that cannot be written is neither passed nor described.
	blocked := newWorld(t, "")
	os.WriteFile(filepath.Join(blocked.alice.home, "harness"), nil, 0o600)
	if got := blocked.alice.questionSetup(job{Kind: envelope.KindQuestion}, "pi"); got.args != nil || got.text != lookupsUnavailable {
		t.Fatalf("unwritable extension %+v", got)
	}
	info, err := os.Stat(args[1])
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("extension file %v %v", info, err)
	}
	// Load it beside Pi's own pi-ai, as Pi resolves it, with a fake host.
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "node_modules", "@earendil-works"), 0o700)
	if err := os.Symlink(piAI, filepath.Join(dir, "node_modules", "@earendil-works", "pi-ai")); err != nil {
		t.Fatal(err)
	}
	src, _ := os.ReadFile(args[1])
	os.WriteFile(filepath.Join(dir, "ext.mjs"), src, 0o600)
	missing := strings.Replace(string(src), exe, filepath.Join(dir, "no-such-agentnet"), 1)
	os.WriteFile(filepath.Join(dir, "missing.mjs"), []byte(missing), 0o600)
	script := `
import ext, { lookupArgs } from "./ext.mjs";
import missing from "./missing.mjs";
const tools = [];
ext({ registerTool: (t) => tools.push(t) });
const t = tools[0];
const out = [];
const fails = async (p, tool = t) => { try { await tool.execute("x", p); return "ran"; } catch (e) { return "failed"; } };
out.push(tools.length, t.name, Object.keys(t.parameters.properties).join(","));
out.push((await t.execute("x", { lookup: "version" })).content[0].text.trim());
out.push((await t.execute("x", { lookup: "inbox" })).content[0].text.trim());
out.push((await t.execute("x", { lookup: "status", id: "0123456789abcdef0123456789abcdef" })).content[0].text.trim());
const page = await t.execute("x", {lookup:"inbox", before:"cursor_123", section:"invites", review:true});
if(page.content[0].text.trim()!=="ran: inbox --peek --before cursor_123 --section invites --review")throw Error("inbox page arguments changed");
const exact = await t.execute("x", {lookup:"inbox", id:"0123456789abcdef0123456789abcdef"});
if(exact.content[0].text.trim()!=="ran: inbox --peek --id 0123456789abcdef0123456789abcdef")throw Error("inbox exact read lost peek");
for(const p of [{lookup:"inbox",before:"x;evil"},{lookup:"inbox",section:"--full"},{lookup:"inbox",id:"0123456789abcdef0123456789abcdef",before:"cursor"},{lookup:"version",before:"cursor"},{lookup:"inbox",review:"false"}])if(await fails(p)!=="failed")throw Error("invalid inbox options ran");
out.push(await fails({ lookup: "status", id: "x; rm -rf /" }));
out.push(await fails({ lookup: "status" }));
out.push(await fails({ lookup: "version", id: "0123456789abcdef0123456789abcdef" }));
out.push(await fails({ lookup: "send" }));
out.push(await fails({ lookup: "__proto__" }));
out.push(await fails({ lookup: "approvals" }));
const m = []; missing({ registerTool: (x) => m.push(x) });
out.push(await fails({ lookup: "version" }, m[0]));
console.log(out.join("|"));
`
	os.WriteFile(filepath.Join(dir, "test.mjs"), []byte(script), 0o600)
	cmd := exec.Command(node, "test.mjs")
	cmd.Dir = dir
	done := make(chan struct{})
	var output []byte
	go func() { output, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		cmd.Process.Kill()
		t.Fatal("node test timed out")
	}
	if err != nil {
		t.Fatalf("node: %v\n%s", err, output)
	}
	want := "1|agentnet_lookup|lookup,id,before,section,review|ran: version|ran: inbox --peek|ran: status 0123456789abcdef0123456789abcdef|failed|failed|failed|failed|failed|failed|failed"
	if got := strings.TrimSpace(string(output)); got != want {
		t.Fatalf("pi lookup tool\n got %s\nwant %s", got, want)
	}
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }

// piPackage is the installed Pi package directory, found beside pi.
func piPackage(t *testing.T) string {
	t.Helper()
	pi, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("pi not installed")
	}
	pi, _ = filepath.EvalSymlinks(pi)
	for dir := filepath.Dir(pi); dir != filepath.Dir(dir); dir = filepath.Dir(dir) {
		if fileExists(filepath.Join(dir, "dist", "index.js")) && fileExists(filepath.Join(dir, "package.json")) {
			return dir
		}
	}
	t.Skip("installed Pi package not found")
	return ""
}

// Pi's own SDK loader, in an isolated session with no other extensions, no
// owner settings and no model request, loads the exact emitted extension:
// its lookup tool is active beside the native built-ins and
// runs the bound program. AGENTNET_LOOKUP_PROGRAM binds a real build.
func TestQuestionLookupPiNativeLoader(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	pkg := piPackage(t)
	w := newWorld(t, "")
	exe := lookupProgram(t)
	if real := os.Getenv("AGENTNET_LOOKUP_PROGRAM"); real != "" {
		exe = real
	}
	bindProgram(t, exe)
	args := w.alice.questionSetup(job{Kind: envelope.KindQuestion}, "pi").args
	if len(args) != 2 {
		t.Fatalf("pi setup args %q", args)
	}
	root := t.TempDir()
	cwd, agentDir := filepath.Join(root, "cwd"), filepath.Join(root, "agent")
	os.MkdirAll(cwd, 0o700)
	os.MkdirAll(agentDir, 0o700)
	script := `
import { createAgentSession, DefaultResourceLoader, SessionManager, SettingsManager } from ` + strconv.Quote("file://"+filepath.Join(pkg, "dist", "index.js")) + `;
const [cwd, agentDir, ext] = process.argv.slice(2);
const settingsManager = SettingsManager.inMemory({});
const resourceLoader = new DefaultResourceLoader({ cwd, agentDir, settingsManager, additionalExtensionPaths: [ext],
	noExtensions: true, noSkills: true, noPromptTemplates: true, noThemes: true, noContextFiles: true });
await resourceLoader.reload();
const loaded = resourceLoader.getExtensions();
const { session } = await createAgentSession({ cwd, agentDir, resourceLoader, settingsManager,
	sessionManager: SessionManager.inMemory(cwd) });
const active = session.getActiveToolNames();
const info = session.getAllTools().find((t) => t.name === "agentnet_lookup");
const def = session.getToolDefinition("agentnet_lookup");
const ran = await def.execute("probe", { lookup: "version" }, undefined, undefined, {});
let refused = "ran";
try { await def.execute("probe", { lookup: "status", id: "$(touch /tmp/x)" }, undefined, undefined, {}); } catch { refused = "refused"; }
console.log(JSON.stringify({ extensions: loaded.extensions.length, errors: loaded.errors.length,
	active: active.sort(), params: Object.keys(info?.parameters?.properties ?? {}), version: ran.content[0].text.split("\n")[0], refused }));
session.dispose();
`
	os.WriteFile(filepath.Join(root, "probe.mjs"), []byte(script), 0o600)
	cmd := exec.Command(node, "probe.mjs", cwd, agentDir, args[1])
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "HOME="+root, "PI_CODING_AGENT_DIR="+agentDir, "AGENTNET_HOME="+filepath.Join(root, "home"))
	out, err := cmd.Output()
	if err != nil {
		var stderr []byte
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = ee.Stderr
		}
		t.Fatalf("pi SDK probe: %v\n%s\n%s", err, out, stderr)
	}
	t.Logf("pi SDK: %s", out)
	var got struct {
		Extensions, Errors int
		Active, Params     []string
		Version, Refused   string
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
		t.Fatal(err)
	}
	if got.Extensions != 1 || got.Errors != 0 || !slices.Contains(got.Active, "agentnet_lookup") || got.Refused != "refused" ||
		!slices.Equal(got.Params, []string{"lookup", "id"}) || got.Version == "" {
		t.Fatalf("native loader %+v", got)
	}
	if os.Getenv("AGENTNET_LOOKUP_PROGRAM") == "" && got.Version != "ran: version" {
		t.Fatalf("lookup ran %q", got.Version)
	}
}

// A named participation agent run by the claude harness gets the same exact
// lookups in its actual argv and its actual prompt.
func TestQuestionLookupParticipationBranch(t *testing.T) {
	w, host, conv, _, _, _, _ := externalAgentWorld(t)
	exe := lookupProgram(t)
	bindProgram(t, exe)
	dir := t.TempDir()
	stub := filepath.Join(dir, "claude-stub")
	os.WriteFile(stub, []byte(stubClaudeLookup), 0o700)
	orig := Harnesses["claude"]
	h := orig
	h.bin = stub
	Harnesses["claude"] = h
	t.Cleanup(func() { Harnesses["claude"] = orig })
	record, err := host.CreateLocalAgent("Lookup reviewer", Responder{Harness: "claude", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invite at host", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "active at alice", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "which agentnet version runs there?")
	if err != nil {
		t.Fatal(err)
	}
	if reply := replyAt(t, w.alice, conv, q.ID); reply.Body != "looked up" {
		t.Fatalf("reply %+v", reply)
	}
	argv, _ := os.ReadFile(stub + ".args")
	prompt, _ := os.ReadFile(stub + ".prompt")
	args := strings.Split(strings.TrimSpace(string(argv)), "\n")
	if slices.Contains(args, "--allowedTools") || slices.Contains(args, "dontAsk") || slices.Contains(args, "--disallowedTools") {
		t.Fatalf("participation changed native policy: %q", args)
	}
	if !strings.Contains(string(prompt), strconv.Quote(exe)) || !strings.Contains(string(prompt), "`whoami`") {
		t.Fatalf("participation prompt lacks lookup hints: %s", prompt)
	}
}

// stubClaudeLookup records its argv (one per line) and prompt, then answers.
const stubClaudeLookup = `#!/bin/sh
printf '%s\n' "$@" > "$0.args"
cat > "$0.prompt"
printf 'looked up\nemotion: calm\n'
`

func TestQuestionLookupOMPUsesNativeReadTool(t *testing.T) {
	w := newWorld(t, "")
	exe := lookupProgram(t)
	bindProgram(t, exe)
	lookup := w.bob.questionSetup(job{Kind: envelope.KindQuestion}, "omp")
	if len(lookup.args) != 2 || lookup.args[0] != "--extension" {
		t.Fatalf("OMP lookup %+v", lookup)
	}
	source, err := os.ReadFile(lookup.args[1])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "parameters: pi.zod.object(") || !strings.Contains(string(source), `approval: "read"`) || strings.Contains(string(source), "@earendil-works") {
		t.Fatalf("not an OMP native read tool: %s", source)
	}
	if !strings.Contains(string(source), strconv.Quote(exe)) {
		t.Fatal("lookup lost exact program binding")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	probe := `import {pathToFileURL} from 'node:url';
 const m=await import(pathToFileURL(` + strconv.Quote(lookup.args[1]) + `).href);
 delete process.env.AGENTNET_ROOM_REQUEST;
 const schema={}; const tools=[];
 m.default({typebox:{},zod:{object:()=>schema,enum:()=>schema,string:()=>({optional:()=>schema}),boolean:()=>({optional:()=>schema})},registerTool:t=>tools.push(t)});
 if(tools.length!==1||tools[0].name!=='agentnet_lookup'||tools[0].approval!=='read')throw Error('OMP tool registration failed');
 const result=await tools[0].execute('lookup',{lookup:'version'});
 if(result.content[0].text.trim()!=='ran: version')throw Error('fixed lookup did not execute');
 const page=await tools[0].execute('page',{lookup:'inbox',before:'cursor_123',section:'invites',review:true});
 if(page.content[0].text.trim()!=='ran: inbox --peek --before cursor_123 --section invites --review')throw Error('OMP inbox page lost read-only arguments');
 let blocked=false;try{await tools[0].execute('lookup',{lookup:'arbitrary'});}catch{blocked=true;}if(!blocked)throw Error('arbitrary operation allowed');
 process.env.AGENTNET_ROOM_REQUEST='0123456789abcdef0123456789abcdef';
 const groupTools=[];m.default({zod:{object:()=>schema,enum:()=>schema,string:()=>({optional:()=>schema}),boolean:()=>({optional:()=>schema})},registerTool:t=>groupTools.push(t)});
 const room=groupTools.find(t=>t.name==='agentnet_room');if(!room||room.approval!=='read')throw Error('OMP scoped room tool missing');
 const answer=await room.execute('room',{action:'ask',pid:process.env.AGENTNET_ROOM_REQUEST,text:'hello'});
 if(!answer.content[0].text.includes(' --kind question -- hello'))throw Error('OMP room operation lost question-only binding');`
	cmd := exec.Command(node, "--input-type=module", "-e", probe)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("OMP API/read-lookup regression: %v %s", e, out)
	}
}

// The native question tool delegates only scoped questions through the exact
// existing room CLI; it provides no shell, task switch or unbound recipient.
func TestQuestionRoomPiTool(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	dir := t.TempDir()
	src := strings.Replace(piLookupSource, "%s", strconv.Quote(lookupProgram(t)), 1)
	if piAI := os.Getenv("AGENTNET_PI_AI"); piAI != "" {
		if err := os.MkdirAll(filepath.Join(dir, "node_modules", "@earendil-works"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(piAI, filepath.Join(dir, "node_modules", "@earendil-works", "pi-ai")); err != nil {
			t.Fatal(err)
		}
	} else {
		src = strings.Replace(src, `import { Type } from "@earendil-works/pi-ai";`, `const Type = new Proxy({}, {get:()=> (...args)=>args});`, 1)
	}
	if err := os.WriteFile(filepath.Join(dir, "ext.mjs"), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	script := `
import assert from 'node:assert/strict';
import ext,{roomArgs} from './ext.mjs';
const pid='0123456789abcdef0123456789abcdef';
delete process.env.AGENTNET_ROOM_REQUEST;
assert.throws(()=>roomArgs({action:'ask',pid,text:'hello'}));
let tools=[];ext({registerTool:t=>tools.push(t)});assert.deepEqual(tools.map(t=>t.name),['agentnet_lookup']);
process.env.AGENTNET_ROOM_REQUEST=pid;
tools=[];ext({registerTool:t=>tools.push(t)});
const room=tools.find(t=>t.name==='agentnet_room');assert.ok(room);
if(process.env.AGENTNET_PI_AI)assert.deepEqual(room.parameters.properties.action.anyOf.map(s=>s.const),['ask','wait']);
assert.deepEqual(roomArgs({action:'ask',pid,text:'-literal question'}),['room','ask','--pid',pid,'--kind','question','--','-literal question']);
assert.deepEqual(roomArgs({action:'wait',id:pid}),['room','wait',pid]);
for(const p of [{action:'task',pid,text:'x'},{action:'ask',pid:'x;evil',text:'x'},{action:'ask',pid,text:''},{action:'wait',id:'x'}])assert.throws(()=>roomArgs(p));
const out=await room.execute('test',{action:'ask',pid,text:'hello'});
assert.equal(out.content[0].text.trim(),'ran: room ask --pid '+pid+' --kind question -- hello');
const abort=new AbortController();abort.abort();
await assert.rejects(room.execute('test',{action:'wait',id:pid},abort.signal));
console.log('room question tool bound, scoped, correlated-wait and cancellation PASS');
`
	os.WriteFile(filepath.Join(dir, "test.mjs"), []byte(script), 0600)
	cmd := exec.Command(node, "test.mjs")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("room tool: %v\n%s", err, out)
	}
}
