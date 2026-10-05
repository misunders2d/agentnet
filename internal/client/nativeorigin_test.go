package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// refusedOrigin: a named session that cannot be the receiver is never
// refused (MEL-537): no receiver, so the answer lands in this computer's
// inbox, with a plain note saying so.
func refusedOrigin(t *testing.T, what string, r *ReplyReceiver, note string) {
	t.Helper()
	if r != nil || !strings.Contains(note, "inbox") {
		t.Fatalf("%s: %+v %q", what, r, note)
	}
}

func duplicateReplySession(t *testing.T, a *Agent, handle string) {
	t.Helper()
	if _, e := a.store.db.Exec(`INSERT INTO reply_sessions(handle,record) SELECT ?, json_set(record,'$.handle',?) FROM reply_sessions WHERE handle=?`, protocol.NewID(), protocol.NewID(), handle); e != nil {
		t.Fatal(e)
	}
}

// A request asked from a registered Claude Code session binds that exact
// session (its handle only) when asked from the profile it registered under;
// selection never looks at the native process, which a sandbox hides, while
// the channel lease still does. Anything less than the exact current
// registration refuses.
func TestNativeOriginClaudeExactSession(t *testing.T) {
	w := newWorld(t, "")
	owner, route := claudeReceiverFixture(t, w.alice)
	sid, projects := owner.SessionID, route.Projects
	r, e := w.alice.claudeOrigin(sid, projects)
	if e != "" || r == nil || r.Kind != "live_session" || r.SessionHandle != owner.Handle {
		t.Fatalf("origin %+v %v", r, e)
	}
	if raw, _ := json.Marshal(r); strings.Contains(string(raw), owner.OwnerToken) {
		t.Fatal("origin receiver carries the native owner token")
	}
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "asked from Claude", ReplyReceiver: r})
	if err != nil {
		t.Fatal(err)
	}
	var bound string
	if err = w.alice.store.db.QueryRow(`SELECT r.receiver FROM reply_receivers r JOIN outbox o ON o.reply_receiver=r.id WHERE o.id=?`, sent.ID).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bound, owner.Handle) || strings.Contains(bound, owner.OwnerToken) {
		t.Fatalf("binding %s", bound)
	}

	// The registering process hidden or gone: selection is unchanged, the
	// native lease (claim-owner provenance) still refuses it.
	set := func(path string, v any) {
		t.Helper()
		if _, e := w.alice.store.db.Exec(`UPDATE reply_sessions SET record=json_set(record,?,?) WHERE handle=?`, path, v, owner.Handle); e != nil {
			t.Fatal(e)
		}
	}
	set("$.claude.pid", 2147483646)
	if r, e = w.alice.claudeOrigin(sid, projects); e != "" || r == nil || r.SessionHandle != owner.Handle {
		t.Fatalf("selection needed the native process: %+v %v", r, e)
	}
	hidden := route
	hidden.PID = 2147483646
	if _, err = w.alice.claudeReplyChannelOwner(sid, hidden); err == nil {
		t.Fatal("lease without the registered native process")
	}
	set("$.claude.pid", route.PID)

	r, e = w.alice.claudeOrigin(protocol.NewID(), projects)
	refusedOrigin(t, "another session", r, e)
	r, e = w.alice.claudeOrigin(sid, filepath.Join(t.TempDir(), "projects"))
	refusedOrigin(t, "another profile", r, e)
	r, e = w.bob.claudeOrigin(sid, projects)
	refusedOrigin(t, "another home", r, e)

	duplicateReplySession(t, w.alice, owner.Handle)
	r, e = w.alice.claudeOrigin(sid, projects)
	refusedOrigin(t, "ambiguous registration", r, e)
	w.alice.store.db.Exec(`DELETE FROM reply_sessions WHERE handle<>?`, owner.Handle)
	set("$.key", "forged")
	r, e = w.alice.claudeOrigin(sid, projects)
	refusedOrigin(t, "another key", r, e)
	set("$.key", w.alice.Self().Fingerprint())
	var realm string
	w.alice.store.db.QueryRow(`SELECT json_extract(record,'$.realm') FROM reply_sessions WHERE handle=?`, owner.Handle).Scan(&realm)
	set("$.realm", "another-realm")
	r, e = w.alice.claudeOrigin(sid, projects)
	refusedOrigin(t, "another realm", r, e)
	set("$.realm", realm)

	// Selection never reads the transcript: its size or content cannot
	// refuse a question (delivery proves ownership again).
	writeClaudeRows(t, owner.File, map[string]any{"type": "user", "sessionId": protocol.NewID(), "message": map[string]any{"role": "user", "content": "another"}})
	if r, e = w.alice.claudeOrigin(sid, projects); e != "" || r == nil {
		t.Fatalf("selection read the transcript: %+v %v", r, e)
	}
	os.Remove(owner.File)
	if r, e = w.alice.claudeOrigin(sid, projects); e != "" || r == nil {
		t.Fatalf("transcript not yet written: %+v %v", r, e)
	}

	// A new generation without AgentNet's channel draining it: the hooks
	// announce the answer instead, from the inbox.
	if _, err = w.alice.registerClaudeReplySession("SessionStart", sid, owner.File, route); err != nil {
		t.Fatal(err)
	}
	r, e = w.alice.claudeOrigin(sid, projects)
	refusedOrigin(t, "no channel in this generation", r, e)
	if !strings.Contains(e, "no AgentNet Claude channel") {
		t.Fatalf("note %q", e)
	}
	if _, err = w.alice.claudeReplyChannelOwner(sid, route); err != nil {
		t.Fatal(err)
	}
	if r, e = w.alice.claudeOrigin(sid, projects); e != "" || r == nil {
		t.Fatalf("channel attached again: %+v %v", r, e)
	}

	if _, err = w.alice.registerClaudeReplySession("SessionEnd", sid, owner.File, route); err != nil {
		t.Fatal(err)
	}
	r, e = w.alice.claudeOrigin(sid, projects)
	refusedOrigin(t, "ended session", r, e)
}

// codexHiddenPID models a native daemon a sandboxed caller cannot see: no
// such process exists, so only its record can name it.
const codexHiddenPID = 2147483646

// codexOriginFixture registers a Codex thread whose rollout lies in a
// synthetic Codex home whose daemon record names the registered daemon.
func codexOriginFixture(t *testing.T, a *Agent) (string, string, codexNativeRoute) {
	t.Helper()
	home, e := filepath.EvalSymlinks(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	dir := filepath.Join(home, "sessions", "2026", "10", "03")
	if e = os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	sid := "01a0f81b-8e3d-7002-a05d-5622e2723fd1"
	file := filepath.Join(dir, "rollout-"+sid+".jsonl")
	header, _ := json.Marshal(map[string]any{"type": "session_meta", "ordinal": 0, "payload": map[string]any{"id": sid, "cli_version": "0.160.0", "source": "vscode"}})
	if e = os.WriteFile(file, append(header, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	route := codexNativeRoute{Home: home, Endpoint: codexEndpoint(home),
		Binary: "/synthetic/codex", SHA256: strings.Repeat("a", 64), PID: codexHiddenPID, Boot: strings.TrimSpace(string(boot)), Ticks: 1}
	writeCodexDaemonRecord(t, home, route.PID, route.Boot, route.Ticks)
	if _, e = a.registerCodexReplySession("SessionStart", sid, file, route); e != nil {
		t.Fatal(e)
	}
	return sid, file, route
}

func writeCodexDaemonRecord(t *testing.T, home string, pid int, boot string, ticks uint64) {
	t.Helper()
	os.MkdirAll(filepath.Join(home, "app-server-daemon"), 0700)
	raw, _ := json.Marshal(map[string]any{"pid": pid, "processIdentity": map[string]any{"bootId": boot, "startTicks": ticks}})
	if e := os.WriteFile(filepath.Join(home, "app-server-daemon", "daemon.pid"), raw, 0600); e != nil {
		t.Fatal(e)
	}
}

// A request asked from a registered Codex thread binds that exact thread
// when asked from its Codex home while that home's daemon record still names
// the registering daemon; never its root session, another home, a changed
// daemon record, an ended thread or a rollout that is no longer that
// thread's. The daemon itself is not visible here, as in Codex's sandbox.
func TestNativeOriginCodexExactThread(t *testing.T) {
	w := newWorld(t, "")
	sid, file, route := codexOriginFixture(t, w.alice)
	home := route.Home
	views, _ := w.alice.ReplySessions()
	if len(views) != 1 {
		t.Fatalf("registry %+v", views)
	}
	r, e := w.alice.codexOrigin(sid, home)
	if e != "" || r == nil || r.Kind != "live_session" || r.SessionHandle != views[0].Handle {
		t.Fatalf("origin %+v %v", r, e)
	}
	var token string
	w.alice.store.db.QueryRow(`SELECT json_extract(record,'$.owner_token') FROM reply_sessions`).Scan(&token)
	if raw, _ := json.Marshal(r); token == "" || strings.Contains(string(raw), token) {
		t.Fatal("origin receiver carries the native owner token")
	}
	// Delivery still needs the registered daemon itself.
	if err := route.verify(tctx(t), false); err == nil {
		t.Fatal("hidden native daemon verified for delivery")
	}

	r, e = w.alice.codexOrigin("root-"+sid, home) // a parent/root session id is another name, never a fallback
	refusedOrigin(t, "another thread", r, e)
	other, _ := filepath.EvalSymlinks(t.TempDir())
	writeCodexDaemonRecord(t, other, route.PID, route.Boot, route.Ticks)
	r, e = w.alice.codexOrigin(sid, other)
	refusedOrigin(t, "another Codex home", r, e)
	r, e = w.bob.codexOrigin(sid, home)
	refusedOrigin(t, "another AgentNet home", r, e)

	for name, record := range map[string]func(){
		"restarted daemon": func() { writeCodexDaemonRecord(t, home, route.PID+1, route.Boot, route.Ticks) },
		"reused pid":       func() { writeCodexDaemonRecord(t, home, route.PID, route.Boot, route.Ticks+1) },
		"after reboot":     func() { writeCodexDaemonRecord(t, home, route.PID, "another-boot", route.Ticks) },
		"no daemon record": func() { os.Remove(filepath.Join(home, "app-server-daemon", "daemon.pid")) },
	} {
		record()
		r, e = w.alice.codexOrigin(sid, home)
		refusedOrigin(t, name, r, e)
	}
	writeCodexDaemonRecord(t, home, route.PID, route.Boot, route.Ticks)

	set := func(path string, v any) {
		t.Helper()
		if _, e := w.alice.store.db.Exec(`UPDATE reply_sessions SET record=json_set(record,?,?) WHERE handle=?`, path, v, views[0].Handle); e != nil {
			t.Fatal(e)
		}
	}
	duplicateReplySession(t, w.alice, views[0].Handle)
	r, e = w.alice.codexOrigin(sid, home)
	refusedOrigin(t, "ambiguous registration", r, e)
	w.alice.store.db.Exec(`DELETE FROM reply_sessions WHERE handle<>?`, views[0].Handle)
	set("$.key", "forged")
	r, e = w.alice.codexOrigin(sid, home)
	refusedOrigin(t, "another key", r, e)
	set("$.key", w.alice.Self().Fingerprint())
	var realm string
	w.alice.store.db.QueryRow(`SELECT json_extract(record,'$.realm') FROM reply_sessions WHERE handle=?`, views[0].Handle).Scan(&realm)
	set("$.realm", "another-realm")
	r, e = w.alice.codexOrigin(sid, home)
	refusedOrigin(t, "another realm", r, e)
	set("$.realm", realm)
	set("$.codex.endpoint", "unix:///elsewhere/app-server-control.sock")
	r, e = w.alice.codexOrigin(sid, home)
	refusedOrigin(t, "another daemon endpoint", r, e)
	set("$.codex.endpoint", route.Endpoint)

	// Selection never reads the rollout: delivery proves it again.
	header, _ := json.Marshal(map[string]any{"type": "session_meta", "ordinal": 0, "payload": map[string]any{"id": "another-thread", "cli_version": "0.160.0", "source": "vscode"}})
	original, _ := os.ReadFile(file)
	os.WriteFile(file, append(header, '\n'), 0600)
	if r, e = w.alice.codexOrigin(sid, home); e != "" || r == nil {
		t.Fatalf("selection read the rollout: %+v %v", r, e)
	}
	os.WriteFile(file, original, 0600)

	if _, err := w.alice.registerCodexReplySession("SessionEnd", sid, file, route); err != nil {
		t.Fatal(err)
	}
	r, e = w.alice.codexOrigin(sid, home)
	refusedOrigin(t, "ended thread", r, e)
}

// The harness environment decides: no name is a plain command (inbox), both
// names are ambiguous, a named session is selected only through the caller's
// own native profile, and anything unqualified goes to this computer's inbox
// with a note: asking is never refused (MEL-537).
func TestNativeOriginNamesOnly(t *testing.T) {
	w := newWorld(t, "")
	if r, e := w.alice.NativeOriginReceiver("", ""); r != nil || e != "" {
		t.Fatalf("plain command %+v %v", r, e)
	}
	r, e := w.alice.NativeOriginReceiver("claude-session", "codex-thread")
	if r != nil || !strings.Contains(e, "cannot tell which one asked") || !strings.Contains(e, "inbox") {
		t.Fatalf("both names %+v %v", r, e)
	}

	owner, route := claudeReceiverFixture(t, w.alice)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Dir(route.Projects))
	t.Setenv("CLAUDECODE", "")
	t.Setenv(ClaudeSessionEnv, owner.SessionID)
	r, e = w.alice.NativeOriginReceiver(owner.SessionID, "")
	refusedOrigin(t, "not started by Claude Code", r, e)
	t.Setenv("CLAUDECODE", "1")
	if r, e = w.alice.NativeOriginReceiver(owner.SessionID, ""); e != "" || r == nil || r.SessionHandle != owner.Handle {
		t.Fatalf("Claude origin from its profile %+v %v", r, e)
	}
	t.Setenv(ClaudeSessionEnv, protocol.NewID())
	r, e = w.alice.NativeOriginReceiver(owner.SessionID, "")
	refusedOrigin(t, "another session's environment", r, e)
	t.Setenv(ClaudeSessionEnv, owner.SessionID)
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	r, e = w.alice.NativeOriginReceiver(owner.SessionID, "")
	refusedOrigin(t, "another Claude profile", r, e)

	sid, _, codex := codexOriginFixture(t, w.alice)
	t.Setenv("CODEX_HOME", codex.Home)
	if r, e = w.alice.NativeOriginReceiver("", sid); e != "" || r == nil {
		t.Fatalf("Codex origin from its home %+v %v", r, e)
	}
	t.Setenv("CODEX_HOME", t.TempDir()) // no running daemon here
	r, e = w.alice.NativeOriginReceiver("", sid)
	refusedOrigin(t, "another Codex home", r, e)
}
