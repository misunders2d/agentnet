package client

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Claude's capture holds its documented subprocess contract before it looks
// for the native process: Claude Code marks what it starts and names the
// session's own ID; anything else is refused.
func TestClaudeRouteCaptureNeedsDocumentedContract(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native provenance")
	}
	for _, tc := range []struct{ name, marker, session, want string }{
		{"not started by Claude Code", "", "sid-1", "CLAUDECODE"},
		{"session ID absent", "1", "", "CLAUDE_CODE_SESSION_ID"},
		{"another session's ID", "1", "sid-2", "CLAUDE_CODE_SESSION_ID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDECODE", tc.marker)
			t.Setenv("CLAUDE_CODE_SESSION_ID", tc.session)
			if _, e := captureClaudeRoute("sid-1"); e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("capture: %v", e)
			}
		})
	}
}

// Any Claude build that keeps that contract is captured with its own exact
// executable digest: no release digest is required.
func TestClaudeRouteCaptureAnyBuildOwnDigest(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux native provenance")
	}
	self, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	claude := filepath.Join(dir, "claude") // this test binary, as a Claude build no pin names
	in, e := os.Open(self)
	if e != nil {
		t.Fatal(e)
	}
	out, e := os.OpenFile(claude, os.O_CREATE|os.O_WRONLY, 0o700)
	if e == nil {
		_, e = io.Copy(out, in)
		out.Close()
	}
	in.Close()
	if e != nil {
		t.Fatal(e)
	}
	config := filepath.Join(dir, "config")
	os.Mkdir(config, 0o700)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE") && !strings.HasPrefix(kv, "CODEX") {
			env = append(env, kv)
		}
	}
	cmd := exec.Command(claude)
	cmd.Env = append(env, "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=sid-any-build", "CLAUDE_CONFIG_DIR="+config, "AGENTNET_TEST_CAPTURE_CLAUDE_ROUTE=sid-any-build")
	raw, e := cmd.Output()
	if e != nil {
		t.Fatalf("%v %s", e, raw)
	}
	var got struct {
		Route claudeNativeRoute `json:"route"`
		Error string            `json:"error"`
	}
	if e = json.Unmarshal(bytes.TrimSpace(raw), &got); e != nil {
		t.Fatalf("%v %s", e, raw)
	}
	digest, _ := codexBinaryDigest(claude)
	real, _ := filepath.EvalSymlinks(config)
	if got.Error != "" || got.Route.Binary != claude || got.Route.SHA256 != digest || got.Route.PID < 1 || got.Route.Projects != filepath.Join(real, "projects") || got.Route.Source != claudeChannelSource {
		t.Fatalf("captured %+v %q", got.Route, got.Error)
	}
}

// A registration is bound to the executable it captured: a changed digest
// (an update in place) ends it until Claude's own hook registers anew.
func TestClaudeRegistrationRefusesChangedExecutable(t *testing.T) {
	w := newWorld(t, "")
	owner, route := claudeReceiverFixture(t, w.alice)
	changed := route
	changed.SHA256 = strings.Repeat("0", 64)
	if _, e := w.alice.claudeReplyChannelOwner(owner.SessionID, changed); e == nil || !strings.Contains(e.Error(), "executable changed") {
		t.Fatalf("lease under a changed executable: %v", e)
	}
	if _, e := w.alice.registerClaudeReplySession("PostToolUse", owner.SessionID, owner.File, changed); e == nil {
		t.Fatal("hook under a changed executable kept the registration")
	}
	if _, e := w.alice.claudeReplyChannelOwner(owner.SessionID, route); e != nil {
		t.Fatalf("exact registration lost: %v", e)
	}
}

// A resumed Claude session keeps earlier versions' records; versions are
// typed, the receipt stays the exact native tuple, and a transcript not yet
// written is not a failure.
func TestClaudeTranscriptAcrossVersions(t *testing.T) {
	sid := "resumed-session"
	ack := ReplyReceiverAck{BindingID: "binding", InputID: "input", ClaimID: "claim", InputToken: "token"}
	file := filepath.Join(t.TempDir(), sid+".jsonl")
	if found, e := claudeNativeScan(file, sid, true, 0, nil); found || e != nil {
		t.Fatalf("lazy transcript: %v %v", found, e)
	}
	older := map[string]any{"type": "user", "uuid": "older", "sessionId": sid, "version": "2.1.200", "message": map[string]any{"role": "user", "content": "earlier prompt"}}
	receipt := claudeReceiptRow(sid, claudeChannelSource, ack)
	receipt["version"] = "2.2.0-beta.1"
	writeClaudeRows(t, file, older, receipt)
	if found, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, 0); !found || e != nil {
		t.Fatalf("receipt across versions: %v %v", found, e)
	}
	for _, v := range []string{"2.1", "latest", "2.1.287; rm"} {
		bad := claudeReceiptRow(sid, claudeChannelSource, ack)
		bad["version"] = v
		writeClaudeRows(t, file, older, bad)
		if _, e := claudeNativeReceipt(file, sid, claudeChannelSource, ack, 0); e == nil {
			t.Fatalf("malformed version %q accepted", v)
		}
	}
}

// A resumed Codex thread keeps the header its creating version wrote; the
// header still names this exact thread from the vscode daemon source.
func TestCodexRolloutAcrossVersions(t *testing.T) {
	sid := "resumed-thread"
	write := func(header map[string]any) string {
		file := filepath.Join(t.TempDir(), "rollout.jsonl")
		data, _ := json.Marshal(header)
		os.WriteFile(file, append(data, '\n'), 0o600)
		return file
	}
	header := func(version, source string) map[string]any {
		payload := map[string]any{"id": sid, "source": source}
		if version != "" {
			payload["cli_version"] = version
		}
		return map[string]any{"type": "session_meta", "ordinal": 0, "payload": payload}
	}
	for _, v := range []string{"0.150.0", "0.160.0", "0.161.0-alpha.2"} {
		if _, e := codexNativeEntries(write(header(v, "vscode")), sid); e != nil {
			t.Fatalf("version %s: %v", v, e)
		}
	}
	for name, h := range map[string]map[string]any{
		"no version": header("", "vscode"), "malformed version": header("0.160", "vscode"), "another source": header("0.160.0", "cli"),
	} {
		if _, e := codexNativeEntries(write(h), sid); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

// The daemon's own typed report must match the registered route; its
// managed and CLI versions agree with each other, not with a release.
func TestCodexDaemonVersionReport(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("unix socket")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	socket := filepath.Join(dir, "control.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	binary := filepath.Join(dir, "codex")
	os.WriteFile(binary, []byte("synthetic"), 0o700)
	route := codexNativeRoute{Binary: binary, Endpoint: "unix://" + socket}
	report := func(change func(map[string]string)) []byte {
		v := map[string]string{"status": "running", "backend": "pid", "socketPath": socket, "managedCodexPath": binary, "managedCodexVersion": "0.160.0", "cliVersion": "0.160.0"}
		if change != nil {
			change(v)
		}
		raw, _ := json.Marshal(v)
		return raw
	}
	if e = codexDaemonVersion(report(nil), route); e != nil {
		t.Fatalf("current version: %v", e)
	}
	if e = codexDaemonVersion(report(func(v map[string]string) { v["managedCodexVersion"], v["cliVersion"] = "0.170.0", "0.170.0" }), route); e != nil {
		t.Fatalf("later version: %v", e)
	}
	for name, change := range map[string]func(map[string]string){
		"versions disagree":   func(v map[string]string) { v["managedCodexVersion"] = "0.159.3" },
		"malformed version":   func(v map[string]string) { v["managedCodexVersion"], v["cliVersion"] = "dev", "dev" },
		"not running":         func(v map[string]string) { v["status"] = "stopped" },
		"another backend":     func(v map[string]string) { v["backend"] = "service" },
		"another socket":      func(v map[string]string) { v["socketPath"] = socket + ".other" },
		"another executable":  func(v map[string]string) { v["managedCodexPath"] = filepath.Join(dir, "other") },
		"no socket at report": func(v map[string]string) { v["socketPath"] = binary },
	} {
		r := route
		if name == "no socket at report" {
			r.Endpoint = "unix://" + binary
		}
		if e = codexDaemonVersion(report(change), r); e == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if e = codexDaemonVersion([]byte("not json"), route); e == nil {
		t.Fatal("unreadable report accepted")
	}
}
