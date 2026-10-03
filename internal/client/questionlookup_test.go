package client

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	path := filepath.Join(t.TempDir(), "agentnet")
	os.WriteFile(path, []byte("#!/bin/sh\n[ \"$1\" = approvals ] && { echo refused >&2; exit 3; }\necho \"ran: $*\"\n"), 0o700)
	resolved, _ := filepath.EvalSymlinks(path)
	return resolved
}

// Claude gets exact allow rules bound to the installed program: fixed
// lookups, and status only for ids this device sent that the request names.
func TestQuestionLookupClaudeExactRules(t *testing.T) {
	if runtime := os.Getenv("GOOS"); runtime == "windows" {
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
	got := setup.args
	want := []string{"--allowedTools",
		"Bash(" + exe + " version)", "Bash(" + exe + " whoami)", "Bash(" + exe + " inbox --peek)", "Bash(" + exe + " inbox --peek --json)", "Bash(" + exe + " approvals)",
		"Bash(" + exe + " status " + sent.ID + ")"}
	if !slices.Equal(got, want) {
		t.Fatalf("claude extras\n got %q\nwant %q", got, want)
	}
	for _, r := range got {
		if strings.Contains(r, "*") || strings.Contains(r, foreign) {
			t.Fatalf("wildcard or foreign id in %q", r)
		}
	}
	// The prompt names exactly what was configured.
	if !strings.Contains(setup.text, exe+" status "+sent.ID) || strings.Contains(setup.text, foreign) || !strings.Contains(setup.text, exe+" inbox --peek") {
		t.Fatalf("claude text %q", setup.text)
	}
	if task := w.alice.questionSetup(job{Kind: envelope.KindTask, Body: sent.ID}, "claude"); task.args != nil || task.text != "" {
		t.Fatalf("task got lookups %+v", task)
	}
	if codex := w.alice.questionSetup(j, "codex"); codex.args != nil || !strings.Contains(codex.text, strconv.Quote(exe)) || !strings.Contains(codex.text, "`inbox --peek`") {
		t.Fatalf("codex %+v", codex)
	}
	// A path that cannot be an exact rule, or no program, gives nothing.
	spaced := filepath.Join(t.TempDir(), "a b")
	os.MkdirAll(spaced, 0o700)
	odd := filepath.Join(spaced, "agentnet")
	os.WriteFile(odd, []byte("#!/bin/sh\n"), 0o700)
	bindProgram(t, odd)
	if got := w.alice.questionSetup(j, "claude"); got.args != nil || got.text != lookupsUnavailable {
		t.Fatalf("unsafe path bound %+v", got)
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
	want := "1|agentnet_lookup|lookup,id|ran: version|ran: inbox --peek|ran: status 0123456789abcdef0123456789abcdef|failed|failed|failed|failed|failed|failed|failed"
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
// its lookup tool is active beside the question's excluded built-ins and
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
	sessionManager: SessionManager.inMemory(cwd), excludeTools: ["bash", "edit", "write", "powershell"] });
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
		slices.ContainsFunc(got.Active, func(n string) bool { return n == "bash" || n == "edit" || n == "write" }) ||
		!slices.Equal(got.Params, []string{"lookup", "id"}) || got.Version == "" {
		t.Fatalf("native loader %+v", got)
	}
	if os.Getenv("AGENTNET_LOOKUP_PROGRAM") == "" && got.Version != "ran: version" {
		t.Fatalf("lookup ran %q", got.Version)
	}
}

// Claude's exact rule spells the program as its command text must: a Unix
// path or a forward-slash Windows drive path, never one needing quoting.
func TestClaudeRulePathPlatforms(t *testing.T) {
	for _, c := range []struct {
		goos, exe, want string
		ok              bool
	}{
		{"linux", "/usr/local/bin/agentnet", "/usr/local/bin/agentnet", true},
		{"darwin", "/opt/homebrew/bin/agentnet", "/opt/homebrew/bin/agentnet", true},
		{"linux", "/home/a b/agentnet", "", false},
		{"linux", "/tmp/x$(id)/agentnet", "", false},
		{"windows", `C:\Users\bob\AppData\Local\agentnet\agentnet.exe`, "C:/Users/bob/AppData/Local/agentnet/agentnet.exe", true},
		{"windows", `C:\Program Files\AgentNet\agentnet.exe`, "", false},
		{"windows", `\\server\share\agentnet.exe`, "", false},
		{"windows", `C:\Users\bob(1)\agentnet.exe`, "", false},
	} {
		got, ok := claudeRulePath(c.goos, c.exe)
		if ok != c.ok || ok && got != c.want {
			t.Errorf("%s %q: %q %v", c.goos, c.exe, got, ok)
		}
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
	at := slices.Index(args, "--allowedTools")
	if at < 0 || !slices.Contains(args[at:], "Bash("+exe+" version)") || !slices.Contains(args, "dontAsk") || slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "*") }) {
		t.Fatalf("participation argv %q", args)
	}
	if !strings.Contains(string(prompt), "you may run exactly these read-only commands") || !strings.Contains(string(prompt), exe+" whoami") {
		t.Fatalf("participation prompt lacks the configured lookups:\n%s", prompt)
	}
}

// stubClaudeLookup records its argv (one per line) and prompt, then answers.
const stubClaudeLookup = `#!/bin/sh
printf '%s\n' "$@" > "$0.args"
cat > "$0.prompt"
printf 'looked up\nemotion: calm\n'
`
