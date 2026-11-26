//go:build linux

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The fake Codex Desktop engine is this test binary run as "codex
// app-server" (TestMain, AN_FAKE_CODEX_APP_SERVER): it keeps the chat's
// queue in $CODEX_HOME/queue.json, as the real engine keeps it in its own
// state, and fails on request.

type codexChatFixture struct {
	w       *world
	home    string
	thread  string
	file    string
	engine  *exec.Cmd
	agentID string
}

func newCodexChatFixture(t *testing.T, notOwn ...bool) *codexChatFixture {
	t.Helper()
	w := newWorld(t, "")
	runAgent(t, w.alice)
	label, name, _ := protocol.SplitAddress(w.alice.Address)
	var prof protocol.Profile
	eventually(t, "synthetic alice session published", func() bool {
		err := w.bob.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof)
		return err == nil && len(prof.Sessions) == 1
	})
	caps := protocol.CapsRecord{Address: w.alice.Address, Session: prof.Sessions[0], Caps: []string{protocol.CapAgentIdentity}, TS: time.Now().Unix() + 100}
	caps.Sign(w.alice.id.Sign)
	if err := w.alice.hub.do(tctx(t), "PUT", "/v1/caps", caps, nil); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if len(notOwn) == 0 || !notOwn[0] {
		chatOwnDevice(t, w)
	} else if err := w.bob.store.pin(w.alice.Self()); err != nil {
		t.Fatal(err)
	}
	f := &codexChatFixture{w: w, thread: "01a21849-d9de-7310-9cf5-f5e06640cecd"}
	f.home = t.TempDir()
	t.Setenv("CODEX_HOME", f.home)
	dir := filepath.Join(f.home, "sessions", "2026", "11", "26")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f.file = filepath.Join(dir, "rollout-2026-11-26T14-37-02-"+f.thread+".jsonl")
	f.append(t, map[string]any{"type": "session_meta", "payload": map[string]any{"id": f.thread, "cwd": f.home, "originator": "Codex Desktop", "cli_version": "0.171.0-alpha.13.2", "source": "vscode"}})
	// The engine: a live process this test controls, run as Desktop's
	// bundled codex (a script that runs this binary as the app server).
	exe := filepath.Join(t.TempDir(), "codex")
	self, _ := os.Executable()
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nAN_FAKE_CODEX_APP_SERVER=1 exec "+self+" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	f.engine = exec.Command("sleep", "300")
	if err := f.engine.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.engine.Process.Kill(); f.engine.Wait() })
	_, ticks, _, err := codexProcess(f.engine.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	engine := codexEngine{PID: f.engine.Process.Pid, Ticks: ticks, Exe: exe}
	old := codexDesktopEngine
	codexDesktopEngine = func(home string) (codexEngine, error) {
		if home != f.home {
			return codexEngine{}, fmt.Errorf("wrong home %s", home)
		}
		if f.engine.ProcessState != nil {
			return codexEngine{}, fmt.Errorf("Codex Desktop is not running")
		}
		return engine, nil
	}
	t.Cleanup(func() { codexDesktopEngine = old })
	oldTrusted := codexTrustedExe
	codexTrustedExe = func(string) error { return nil } // the fake engine lives in a test folder
	t.Cleanup(func() { codexTrustedExe = oldTrusted })
	oldSettle, oldRetries := codexDeleteSettle, codexWithdrawRetries
	codexDeleteSettle, codexWithdrawRetries = 150*time.Millisecond, []time.Duration{50 * time.Millisecond, 100 * time.Millisecond}
	oldBackoff := codexWithdrawBackoff
	codexWithdrawBackoff = []time.Duration{100 * time.Millisecond}
	t.Cleanup(func() { codexWithdrawBackoff = oldBackoff })
	t.Cleanup(func() { codexDeleteSettle, codexWithdrawRetries = oldSettle, oldRetries })
	if f.agentID, err = w.bob.ConnectCodexChat(f.thread, "Codex Desktop"); err != nil {
		t.Fatal(err)
	}
	return f
}

// append writes one rollout record, as Codex Desktop does.
func (f *codexChatFixture) append(t *testing.T, rec map[string]any) {
	t.Helper()
	fakeQueueMu.Lock()
	defer fakeQueueMu.Unlock()
	if err := appendRollout(f.file, rec); err != nil {
		t.Fatal(err)
	}
}

func (f *codexChatFixture) event(t *testing.T, payload map[string]any) {
	f.append(t, map[string]any{"type": "event_msg", "payload": payload})
}

func (f *codexChatFixture) queue() []fakeQueueEntry {
	return readFakeQueue(filepath.Join(f.home, "queue.json"))
}

// dispatch is Codex Desktop starting the queued entry: it leaves the queue
// and its user message is recorded in a new turn.
func (f *codexChatFixture) dispatch(t *testing.T) (fakeQueueEntry, string) {
	t.Helper()
	var e fakeQueueEntry
	eventually(t, "request queued", func() bool {
		q := f.queue()
		if len(q) == 1 {
			e = q[0]
			return true
		}
		return false
	})
	writeFakeQueue(filepath.Join(f.home, "queue.json"), nil)
	turn := protocol.NewID()
	f.event(t, map[string]any{"type": "task_started", "turn_id": turn})
	f.event(t, map[string]any{"type": "item_completed", "thread_id": f.thread, "turn_id": turn, "item": map[string]any{"type": "UserMessage", "id": protocol.NewID(), "client_id": e.ClientID,
		"content": []map[string]any{{"type": "text", "text": e.Text}}}})
	return e, turn
}

func (f *codexChatFixture) ask(t *testing.T, body string) string {
	t.Helper()
	in := envelope.Inner{ID: protocol.NewID(), From: f.w.alice.Address, To: f.w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: body,
		Target: &envelope.Target{Address: f.w.bob.Address, Fingerprint: f.w.bob.Self().Fingerprint(), AgentID: f.agentID}}
	if err := f.w.alice.store.addOutbox(envelope.Envelope{ID: in.ID, From: in.From, To: in.To}, in, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.w.bob.store.addInbox(in, f.w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	return in.ID
}

func (f *codexChatFixture) run(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() { defer close(done); f.w.bob.runNext(ctx, nil) }()
	return done
}

func (f *codexChatFixture) answer(t *testing.T, request string) string {
	t.Helper()
	var answerID string
	if err := f.w.bob.store.db.QueryRow(`SELECT result_id FROM inbox WHERE id=?`, request).Scan(&answerID); err != nil {
		t.Fatal(err)
	}
	env, err := f.w.bob.store.outboxEnvelope(answerID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.w.alice.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	return inboxRow(t, f.w.alice, answerID).Body
}

func TestCodexChatAnswersOnlyItsExactTurn(t *testing.T) {
	f := newCodexChatFixture(t)
	request := f.ask(t, "PROBE-1 what is two plus two?")
	done := f.run(tctx(t))
	// The person's own turn completes first: not this request's answer.
	own := protocol.NewID()
	f.event(t, map[string]any{"type": "task_started", "turn_id": own})
	f.event(t, map[string]any{"type": "item_completed", "thread_id": f.thread, "turn_id": own, "item": map[string]any{"type": "UserMessage", "id": protocol.NewID(), "client_id": protocol.NewID(),
		"content": []map[string]any{{"type": "text", "text": "the person's own message"}}}})
	f.event(t, map[string]any{"type": "task_complete", "turn_id": own, "last_agent_message": "THE PERSON'S OWN ANSWER"})
	e, turn := f.dispatch(t)
	if !strings.Contains(e.Text, "PROBE-1") || e.Thread != f.thread {
		t.Fatalf("queued %+v", e)
	}
	eventually(t, "turn started", func() bool {
		row, _, _ := codexChatRowIn(f.w.bob.store.db, request)
		return row.State == chatStarted && row.Turn == turn
	})
	f.event(t, map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "CHAT-ANSWER four"})
	waitDone(t, done)
	if s, _ := f.w.bob.store.jobState(request); s != stateAnswered {
		t.Fatalf("state %s: %+v", s, inboxRow(t, f.w.bob, request))
	}
	if got := f.answer(t, request); !strings.Contains(got, "CHAT-ANSWER four") || strings.Contains(got, "PERSON") {
		t.Fatalf("answer %q", got)
	}
	if q := f.queue(); len(q) != 0 {
		t.Fatalf("queue %+v", q)
	}

	// A follow-up reaches the same chat.
	follow := f.ask(t, "PROBE-2 and the previous answer?")
	done = f.run(tctx(t))
	e, turn = f.dispatch(t)
	if !strings.Contains(e.Text, "PROBE-2") {
		t.Fatalf("follow-up queued %+v", e)
	}
	f.event(t, map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "CHAT-ANSWER again four"})
	waitDone(t, done)
	if s, _ := f.w.bob.store.jobState(follow); s != stateAnswered {
		t.Fatalf("follow-up state %s", s)
	}
}

func TestCodexChatReplacedTurnIsNotAnAnswer(t *testing.T) {
	f := newCodexChatFixture(t)
	request := f.ask(t, "PROBE-S")
	done := f.run(tctx(t))
	f.dispatch(t)
	// The turn is interrupted and another one starts before it completes.
	other := protocol.NewID()
	f.event(t, map[string]any{"type": "task_started", "turn_id": other})
	f.event(t, map[string]any{"type": "task_complete", "turn_id": other, "last_agent_message": "SOMETHING ELSE"})
	waitDone(t, done)
	if r := inboxRow(t, f.w.bob, request); r.State != stateNeedHuman || !strings.Contains(r.Detail, "without a recorded answer") {
		t.Fatalf("replaced turn %+v", r)
	}
}

func TestCodexChatUnstartedRequestLeavesTheQueue(t *testing.T) {
	f := newCodexChatFixture(t)
	old := codexChatDeadline
	codexChatDeadline = 700 * time.Millisecond
	t.Cleanup(func() { codexChatDeadline = old })
	// The chat is not open in Desktop: nothing starts it before the deadline.
	request := f.ask(t, "PROBE-D")
	waitDone(t, f.run(tctx(t)))
	if r := inboxRow(t, f.w.bob, request); r.State != stateNotRun || !strings.Contains(r.Detail, "only for a chat it has opened") {
		t.Fatalf("unstarted request %+v", r)
	}
	if q := f.queue(); len(q) != 0 {
		t.Fatalf("left in the queue: %+v", q)
	}
	codexChatDeadline = old

	// Desktop closes while a request waits: it leaves the queue too.
	closed := f.ask(t, "PROBE-C")
	done := f.run(tctx(t))
	eventually(t, "queued", func() bool { return len(f.queue()) == 1 })
	f.engine.Process.Kill()
	f.engine.Wait()
	waitDone(t, done)
	if r := inboxRow(t, f.w.bob, closed); r.State != stateNotRun || !strings.Contains(r.Detail, "closed before it started") {
		t.Fatalf("closed Desktop %+v", r)
	}
	if q := f.queue(); len(q) != 0 {
		t.Fatalf("left in the queue: %+v", q)
	}
	// With Desktop closed, a new request is not queued at all.
	late := f.ask(t, "PROBE-L")
	waitDone(t, f.run(tctx(t)))
	if r := inboxRow(t, f.w.bob, late); r.State != stateNotRun || !strings.Contains(r.Detail, "not running") {
		t.Fatalf("closed Desktop request %+v", r)
	}
	if _, ok, _ := codexChatRowIn(f.w.bob.store.db, late); ok {
		t.Fatal("a ledger row was written while Desktop was closed")
	}
}

func TestCodexChatNeverQueuesTwice(t *testing.T) {
	f := newCodexChatFixture(t)
	// The add succeeds but its reply is lost: AgentNet finds its own
	// entry by client ID instead of adding again.
	os.WriteFile(filepath.Join(f.home, "drop-reply"), nil, 0600)
	request := f.ask(t, "PROBE-R")
	ctx, stop := context.WithCancel(tctx(t))
	done := f.run(ctx)
	eventually(t, "reconciled", func() bool {
		row, _, _ := codexChatRowIn(f.w.bob.store.db, request)
		return row.State == codexQueued && row.QueueID != ""
	})
	os.Remove(filepath.Join(f.home, "drop-reply"))
	if q := f.queue(); len(q) != 1 {
		t.Fatalf("queue %+v", q)
	}
	// Desktop starts it, then the daemon stops while the turn runs.
	_, turn := f.dispatch(t)
	eventually(t, "started", func() bool {
		row, _, _ := codexChatRowIn(f.w.bob.store.db, request)
		return row.State == chatStarted
	})
	stop()
	waitDone(t, done)
	if s, _ := f.w.bob.store.jobState(request); s != stateInterrupt {
		t.Fatalf("state %s", s)
	}
	// It answers while AgentNet is down; the rerun reads it, adding nothing.
	f.event(t, map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "CHAT-ANSWER while down"})
	if err := f.w.bob.Accept(request); err != nil {
		t.Fatal(err)
	}
	waitDone(t, f.run(tctx(t)))
	if s, _ := f.w.bob.store.jobState(request); s != stateAnswered {
		t.Fatalf("rerun state %s: %+v", s, inboxRow(t, f.w.bob, request))
	}
	if q := f.queue(); len(q) != 0 {
		t.Fatalf("queued again: %+v", q)
	}
}

func TestCodexChatRefusedAddIsNotSent(t *testing.T) {
	f := newCodexChatFixture(t)
	os.WriteFile(filepath.Join(f.home, "refuse"), nil, 0600)
	request := f.ask(t, "PROBE-X")
	waitDone(t, f.run(tctx(t)))
	if r := inboxRow(t, f.w.bob, request); r.State != stateJobFailed || !strings.Contains(r.Detail, "Codex refused") {
		t.Fatalf("refused add %+v", r)
	}
	if row, _, _ := codexChatRowIn(f.w.bob.store.db, request); row.State != chatUnsent {
		t.Fatalf("ledger %+v", row)
	}
}

func TestCodexChatTakesOnlyTheOwnersDevices(t *testing.T) {
	f := newCodexChatFixture(t, true)
	request := f.ask(t, "PROBE-O from someone else")
	waitDone(t, f.run(tctx(t)))
	if r := inboxRow(t, f.w.bob, request); r.State != stateNotRun || !strings.Contains(r.Detail, "only from your own devices") {
		t.Fatalf("other person's request %+v", r)
	}
	if _, ok, _ := codexChatRowIn(f.w.bob.store.db, request); ok || len(f.queue()) != 0 {
		t.Fatal("another person's request was queued")
	}
}

func TestCodexChatConnectOnlyDesktopThreads(t *testing.T) {
	f := newCodexChatFixture(t)
	// A CLI thread in the same home is not a Codex Desktop chat.
	cli := "01a2184b-3b74-7750-a56a-00e92c0b1af6"
	file := filepath.Join(filepath.Dir(f.file), "rollout-2026-11-26T14-38-33-"+cli+".jsonl")
	raw, _ := json.Marshal(map[string]any{"type": "session_meta", "ordinal": 0, "payload": map[string]any{"id": cli, "cwd": f.home, "originator": "codex_cli_rs", "cli_version": "0.172.0", "source": "vscode"}})
	os.WriteFile(file, append(raw, '\n'), 0600)
	if _, err := f.w.bob.ConnectCodexChat(cli, "cli"); err == nil || !strings.Contains(err.Error(), "not started in Codex Desktop") {
		t.Fatalf("CLI thread connected: %v", err)
	}
	if _, err := f.w.bob.ConnectCodexChat("../../etc", "x"); err == nil {
		t.Fatal("a path was taken as a thread")
	}
	// Reconnecting the same chat keeps one agent; it cannot be the default.
	again, err := f.w.bob.ConnectCodexChat(f.thread, "Codex Desktop")
	if err != nil || again != f.agentID {
		t.Fatalf("reconnect %s %v", again, err)
	}
	if err := f.w.bob.SetDefaultAgent(f.agentID); err == nil {
		t.Fatal("a Codex Desktop chat became the default agent")
	}
	if err := validateResponder(&Responder{Harness: CodexChatHarness, Dir: f.home}); err == nil {
		t.Fatal("a codex-chat agent without its chat was accepted")
	}
}

func TestWatchFileWritesWakesOnAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f")
	os.WriteFile(path, nil, 0600)
	w, err := watchFileWrites(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.close()
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	file.Write([]byte("x\n"))
	file.Close()
	select {
	case <-w.changes:
	case <-time.After(5 * time.Second):
		t.Fatal("no wake on write")
	}
}

func TestCodexChatRefusesAnAgentAskingForAnotherPerson(t *testing.T) {
	stub := installAgentStub(t)
	f := newCodexChatFixture(t, true)
	a := f.w.bob
	persons(t, a)
	runAgent(t, a)
	phone := linked(t, a)
	publishGroupFixtureCaps(t, a, true)
	publishGroupFixtureCaps(t, phone, true)
	asker, err := a.CreateLocalAgent("background", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	// Another person's admitted question runs in a different agent; its
	// helper ask is posted with the laptop's own key and local job.
	cause := protocol.NewID()
	if _, err = a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,agent_id,attempts,last_attempt_at)
		VALUES(?,?,1,?,?,1,?,?,?,1,unixepoch())`, cause, f.w.alice.Address, envelope.KindQuestion, "look up a rate",
		stateRunning, f.w.alice.Self().Fingerprint(), asker.ID); err != nil {
		t.Fatal(err)
	}
	sent, err := a.RunAgentRoomAsk(tctx(t), cause, AgentRoomAsk{Host: a.Address, AgentID: f.agentID,
		Kind: envelope.KindQuestion, Body: "ask the connected Codex chat for the rate"})
	if err != nil {
		t.Fatal(err)
	}
	var id string
	if err := a.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND lid=? AND replica=0`, sent.Conv, sent.LID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	waitState(t, a, id, stateNotRun)
	if _, ok, err := codexChatRowIn(a.store.db, id); ok || err != nil || len(f.queue()) != 0 {
		t.Fatalf("helper ask reached the Codex chat: %v, %v, %+v", ok, err, f.queue())
	}
	// The owner can still ask the same agent directly on this computer.
	direct, err := a.AskAgentInTopic(tctx(t), sent.To, envelope.KindQuestion, "owner asking directly", sent.Topic, nil)
	if err != nil {
		t.Fatal(err)
	}
	e, turn := f.dispatch(t)
	if !strings.Contains(e.Text, "owner asking directly") {
		t.Fatalf("wrong request queued: %q", e.Text)
	}
	f.event(t, map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "owner's answer"})
	waitState(t, a, direct.ID, stateAnswered)
}

func TestCodexChatKeepsWithdrawingWhatItCouldNotRemove(t *testing.T) {
	f := newCodexChatFixture(t)
	old := codexChatDeadline
	codexChatDeadline = 500 * time.Millisecond
	t.Cleanup(func() { codexChatDeadline = old })
	// Desktop does not start it, and deleting it fails every time: the
	// request says it may still run, and stays queued in the ledger.
	fails := filepath.Join(f.home, "delete-fails")
	os.WriteFile(fails, nil, 0600)
	stuck := f.ask(t, "PROBE-W")
	waitDone(t, f.run(tctx(t)))
	if r := inboxRow(t, f.w.bob, stuck); r.State != stateNeedHuman || !strings.Contains(r.Detail, "may still run it when you open that chat") {
		t.Fatalf("undeleted request %+v", r)
	}
	if row, _, _ := codexChatRowIn(f.w.bob.store.db, stuck); row.State != codexQueued || len(f.queue()) != 1 {
		t.Fatalf("ledger %+v queue %+v", row, f.queue())
	}
	// The daemon stops while another request waits and the delete still
	// fails: its end says the same, from the ledger.
	stopped := f.ask(t, "PROBE-X")
	ctx, stop := context.WithCancel(tctx(t))
	done := f.run(ctx)
	eventually(t, "second queued", func() bool { return len(f.queue()) == 2 })
	stop()
	waitDone(t, done)
	if r := inboxRow(t, f.w.bob, stopped); r.State != stateInterrupt || !strings.Contains(r.Detail, "may still run it") {
		t.Fatalf("stopped request %+v", r)
	}
	// Once deleting works, the daemon's start takes both out; neither ran.
	os.Remove(fails)
	f.w.bob.withdrawCodexChats(tctx(t))
	if q := f.queue(); len(q) != 0 {
		t.Fatalf("left in the queue: %+v", q)
	}
	for _, id := range []string{stuck, stopped} {
		if row, _, _ := codexChatRowIn(f.w.bob.store.db, id); row.State != codexRemoved {
			t.Fatalf("%s ledger %+v", id, row)
		}
	}
}

func TestCodexChatDeleteCrossingAStartIsNotRemoved(t *testing.T) {
	f := newCodexChatFixture(t)
	old := codexChatDeadline
	codexChatDeadline = 500 * time.Millisecond
	t.Cleanup(func() { codexChatDeadline = old })
	// Desktop starts it just as AgentNet takes it out at the deadline: it
	// is not "taken out and not sent".
	os.WriteFile(filepath.Join(f.home, "dispatch-on-list"), []byte(f.file), 0600)
	request := f.ask(t, "PROBE-X2")
	waitDone(t, f.run(tctx(t)))
	r := inboxRow(t, f.w.bob, request)
	if r.State != stateNeedHuman || strings.Contains(r.Detail, "taken out") {
		t.Fatalf("crossed request %+v", r)
	}

	// A rerun of a request recorded as removed whose start the history
	// shows waits for that turn instead of queueing it again.
	os.Remove(filepath.Join(f.home, "dispatch-on-list"))
	codexChatDeadline = old
	late := f.ask(t, "PROBE-X3")
	ctx, stop := context.WithCancel(tctx(t))
	done := f.run(ctx)
	eventually(t, "queued", func() bool { return len(f.queue()) == 1 })
	stop()
	waitDone(t, done)
	row, _, _ := codexChatRowIn(f.w.bob.store.db, late)
	if row.State != codexRemoved || !strings.Contains(inboxRow(t, f.w.bob, late).Detail, "taken out of the chat's queue") {
		t.Fatalf("not removed %+v %+v", row, inboxRow(t, f.w.bob, late))
	}
	turn := protocol.NewID()
	f.event(t, map[string]any{"type": "task_started", "turn_id": turn})
	f.event(t, map[string]any{"type": "item_completed", "thread_id": f.thread, "turn_id": turn, "item": map[string]any{"type": "UserMessage", "id": protocol.NewID(), "client_id": row.ClientID,
		"content": []map[string]any{{"type": "text", "text": "x"}}}})
	if err := f.w.bob.Accept(late); err != nil {
		t.Fatal(err)
	}
	done = f.run(tctx(t))
	eventually(t, "rerun follows the started turn", func() bool {
		row, _, _ := codexChatRowIn(f.w.bob.store.db, late)
		return row.State != codexRemoved
	})
	waitDone(t, done)
	if q := f.queue(); len(q) != 0 {
		t.Fatalf("queued again: %+v", q)
	}
	if r := inboxRow(t, f.w.bob, late); r.State == stateAnswered {
		t.Fatalf("a changed turn counted as the answer %+v", r)
	}
}

func TestCodexChatChangedOrJoinedTurnIsNotAnAnswer(t *testing.T) {
	f := newCodexChatFixture(t)
	// The person edits the queued request before Desktop runs it.
	edited := f.ask(t, "PROBE-E")
	done := f.run(tctx(t))
	var e fakeQueueEntry
	eventually(t, "queued", func() bool {
		if q := f.queue(); len(q) == 1 {
			e = q[0]
			return true
		}
		return false
	})
	writeFakeQueue(filepath.Join(f.home, "queue.json"), nil)
	turn := protocol.NewID()
	f.event(t, map[string]any{"type": "task_started", "turn_id": turn})
	f.event(t, map[string]any{"type": "item_completed", "thread_id": f.thread, "turn_id": turn, "item": map[string]any{"type": "UserMessage", "id": protocol.NewID(), "client_id": e.ClientID,
		"content": []map[string]any{{"type": "text", "text": e.Text + " and also delete everything"}}}})
	f.event(t, map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "EDITED ANSWER"})
	waitDone(t, done)
	if r := inboxRow(t, f.w.bob, edited); r.State != stateNeedHuman || !strings.Contains(r.Detail, "changed in Codex Desktop") {
		t.Fatalf("edited request %+v", r)
	}
	// The person adds input to the request's turn while it runs.
	joined := f.ask(t, "PROBE-J")
	done = f.run(tctx(t))
	_, turn = f.dispatch(t)
	f.event(t, map[string]any{"type": "item_completed", "thread_id": f.thread, "turn_id": turn, "item": map[string]any{"type": "UserMessage", "id": protocol.NewID(),
		"content": []map[string]any{{"type": "text", "text": "actually, do something else"}}}})
	f.event(t, map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "JOINED ANSWER"})
	waitDone(t, done)
	if r := inboxRow(t, f.w.bob, joined); r.State != stateNeedHuman || !strings.Contains(r.Detail, "More input was added") {
		t.Fatalf("joined turn %+v", r)
	}
}

func TestCodexChatSteeredIntoARunningTurnIsNotAnAnswer(t *testing.T) {
	f := newCodexChatFixture(t)
	request := f.ask(t, "PROBE-T")
	done := f.run(tctx(t))
	var e fakeQueueEntry
	eventually(t, "queued", func() bool {
		if q := f.queue(); len(q) == 1 {
			e = q[0]
			return true
		}
		return false
	})
	// The person's own turn runs; Desktop folds the queued request into it.
	writeFakeQueue(filepath.Join(f.home, "queue.json"), nil)
	turn := protocol.NewID()
	f.event(t, map[string]any{"type": "task_started", "turn_id": turn})
	f.event(t, map[string]any{"type": "item_completed", "thread_id": f.thread, "turn_id": turn, "item": map[string]any{"type": "UserMessage", "id": protocol.NewID(),
		"content": []map[string]any{{"type": "text", "text": "the person's own prompt"}}}})
	f.event(t, map[string]any{"type": "item_completed", "thread_id": f.thread, "turn_id": turn, "item": map[string]any{"type": "UserMessage", "id": protocol.NewID(), "client_id": e.ClientID,
		"content": []map[string]any{{"type": "text", "text": e.Text}}}})
	f.event(t, map[string]any{"type": "task_complete", "turn_id": turn, "last_agent_message": "THE PERSON'S ANSWER"})
	waitDone(t, done)
	if r := inboxRow(t, f.w.bob, request); r.State != stateNeedHuman || !strings.Contains(r.Detail, "already running") {
		t.Fatalf("folded request %+v", r)
	}
}

func TestCodexChatEntryGoneByOtherHandsIsNeverQueuedAgain(t *testing.T) {
	f := newCodexChatFixture(t)
	old := codexChatDeadline
	codexChatDeadline = 500 * time.Millisecond
	t.Cleanup(func() { codexChatDeadline = old })
	// Desktop takes the entry off the queue but has not yet recorded it
	// when the deadline comes: AgentNet did not remove it, so it is not
	// "taken out", and nothing queues it again.
	request := f.ask(t, "PROBE-G")
	done := f.run(tctx(t))
	eventually(t, "queued", func() bool { return len(f.queue()) == 1 })
	writeFakeQueue(filepath.Join(f.home, "queue.json"), nil)
	waitDone(t, done)
	r := inboxRow(t, f.w.bob, request)
	if r.State != stateNeedHuman || !strings.Contains(r.Detail, "without AgentNet removing it") {
		t.Fatalf("gone request %+v", r)
	}
	if row, _, _ := codexChatRowIn(f.w.bob.store.db, request); row.State != chatSuperseded {
		t.Fatalf("ledger %+v", row)
	}
}

func TestCodexChatWithdrawRetriesWhileTheDaemonRuns(t *testing.T) {
	f := newCodexChatFixture(t)
	fails := filepath.Join(f.home, "delete-fails")
	os.WriteFile(fails, nil, 0600)
	request := f.ask(t, "PROBE-K")
	ctx, stop := context.WithCancel(tctx(t))
	done := f.run(ctx)
	eventually(t, "queued", func() bool { return len(f.queue()) == 1 })
	stop()
	waitDone(t, done)
	// No new request comes; the daemon's own retries take it out once the
	// queue can be changed again.
	loop, end := context.WithCancel(tctx(t))
	finished := make(chan struct{})
	go func() { defer close(finished); f.w.bob.keepWithdrawingCodexChats(loop) }()
	defer func() { end(); <-finished }()
	time.Sleep(300 * time.Millisecond)
	if row, _, _ := codexChatRowIn(f.w.bob.store.db, request); row.State != codexQueued {
		t.Fatalf("ledger while failing %+v", row)
	}
	os.Remove(fails)
	eventually(t, "withdrawn by a retry", func() bool {
		row, _, _ := codexChatRowIn(f.w.bob.store.db, request)
		return row.State == codexRemoved && len(f.queue()) == 0
	})
}
