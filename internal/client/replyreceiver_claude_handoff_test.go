package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func handoffMutationCounts(t *testing.T, a *Agent) []int {
	t.Helper()
	var counts []int
	for _, table := range []string{"outbox", "reply_receivers", "reply_receiver_inputs", "uploads", "inbox", "attachments", "task_grants"} {
		var n int
		if e := a.store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil {
			t.Fatal(e)
		}
		counts = append(counts, n)
	}
	return counts
}

func TestClaudeNewClosedReceiverRefusedBeforeSend(t *testing.T) {
	stub := installAgentStub(t)
	w := newWorld(t, "")
	backup, e := w.alice.CreateLocalAgent("backup", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	owner, _ := claudeReceiverFixture(t, w.alice)
	r := ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle, OnClose: &ManagedReplyHandoff{AgentID: backup.ID, Instructions: "local plan", Mode: envelope.KindTask}}
	before := handoffMutationCounts(t, w.alice)
	want, _ := json.Marshal(r)
	file := filepath.Join(t.TempDir(), "selected.txt")
	os.WriteFile(file, []byte("selected exact bytes"), 0600)
	if _, e = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "request", Files: []string{file}, ReplyReceiver: &r}); e == nil || !strings.Contains(e.Error(), "Claude closed-session backup is unavailable") {
		t.Fatalf("unsupported backup: %v", e)
	}
	got, _ := json.Marshal(r)
	bytes, e := os.ReadFile(file)
	if e != nil || string(bytes) != "selected exact bytes" || string(got) != string(want) || !reflect.DeepEqual(before, handoffMutationCounts(t, w.alice)) {
		t.Fatal("refused selection consumed file, changed draft or mutated send/grant state")
	}
	r.OnClose = nil
	if _, e = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "ordinary Claude selected", ReplyReceiver: &r}); e != nil {
		t.Fatal(e)
	}
	for _, harness := range []string{"pi", "omp"} {
		native, _ := nativeReceiverFixture(t, w.alice, harness)
		closedRequest(t, w.alice, w.bob, native.Handle, backup.ID)
	}
}

func TestClaudeNewRemoteClosedReceiverRefusedBeforeImport(t *testing.T) {
	w, phone, stopPhone, stopHost := remoteReceiverWorld(t)
	stopPhone()
	stopHost()
	owner, _ := claudeReceiverFixture(t, w.alice)
	selected := ReplyReceiver{Host: &ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}, Kind: "live_session", SessionHandle: owner.Handle, OnClose: &ManagedReplyHandoff{AgentID: strings.Repeat("a", 32), Instructions: "local plan", Mode: envelope.KindTask}}
	file := filepath.Join(t.TempDir(), "delegated.txt")
	os.WriteFile(file, []byte("private delegated bytes"), 0600)
	if _, e := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "frozen original", Files: []string{file}, ReplyReceiver: &selected}); e != nil {
		t.Fatal(e)
	}
	b := receiverBindings(t, phone)[0]
	full, e := replyReceiverIn(phone.store.db, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	var raw string
	if e = phone.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, full.remote.Route.DelegationID).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var env envelope.Envelope
	if e = json.Unmarshal([]byte(raw), &env); e != nil {
		t.Fatal(e)
	}
	in, e := envelope.Open(env, w.alice.id, w.alice.Address, phone.Self())
	if e != nil {
		t.Fatal(e)
	}
	before := handoffMutationCounts(t, w.alice)
	if e = w.alice.receiverSetupSender(w.alice.store.db, in, phone.Self().Fingerprint()); e == nil || !strings.Contains(e.Error(), "Claude closed-session backup is unavailable") {
		t.Fatalf("remote unsupported backup: %v", e)
	}
	if e = w.alice.verifyAndStore(tctx(t), env); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, handoffMutationCounts(t, w.alice)) {
		t.Fatal("unsupported remote selection stored setup/files/import/ready or granted key")
	}
	var reason string
	if e = w.alice.store.db.QueryRow(`SELECT reason FROM quarantine WHERE id=?`, in.ID).Scan(&reason); e != nil || reason != reasonInvalid {
		t.Fatalf("unsupported remote setup not held: %s %v", reason, e)
	}
}

func TestClaudePersistedClosedReceiverNotReset(t *testing.T) {
	stub := installAgentStub(t)
	w := newWorld(t, "")
	backup, e := w.alice.CreateLocalAgent("backup", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	owner, route := claudeReceiverFixture(t, w.alice)
	nativeInput(t, w.alice, w.bob, owner.Handle)
	b := receiverBindings(t, w.alice)[0]
	full, e := replyReceiverIn(w.alice.store.db, b.ID)
	if e != nil {
		t.Fatal(e)
	}
	// Model a previously persisted, explicit backup; the new-selection guard
	// must neither rewrite it nor manufacture Claude shutdown authority.
	full.Receiver.OnClose = &ManagedReplyHandoff{AgentID: backup.ID, Instructions: "original frozen plan", Mode: envelope.KindTask}
	full.Executor, e = w.alice.ResolveExecutorIn(w.alice.store.db, backup.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	full.handoff = &closedHandoffState{OriginSession: owner.Handle, Preset: receiverPreset(full.Executor, envelope.KindTask)}
	raw, _ := encodeReceiverBinding(full)
	stamp, _ := json.Marshal(full.Executor)
	if _, e = w.alice.store.db.Exec(`UPDATE reply_receivers SET receiver=?,executor=? WHERE id=?`, string(raw), string(stamp), b.ID); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(w.alice.home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	reuse, e := reopened.ReplyReceiverForBinding(b.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = reopened.prepareReplyReceiver(reuse); e != nil {
		t.Fatal("existing frozen binding refused", e)
	}
	var retained string
	reopened.store.db.QueryRow(`SELECT receiver FROM reply_receivers WHERE id=?`, b.ID).Scan(&retained)
	if retained != string(raw) {
		t.Fatal("opening/reusing binding reset persisted delegation")
	}
	if _, e = reopened.registerClaudeReplySession("SessionEnd", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	if e = reopened.reconcileClosedReplyReceiverJobs(); e != nil {
		t.Fatal(e)
	}
	after, e := replyReceiverIn(reopened.store.db, b.ID)
	if e != nil || after.Receiver.Kind != "live_session" || !sameOnClose(after.Receiver.OnClose, full.Receiver.OnClose) || after.HandoffState == "handed_over" {
		t.Fatal("detached legacy Claude binding redirected or reset")
	}
	if _, ok, e := reopened.claimReplyReceiverJob(); e != nil || ok || stub.runs() != 0 {
		t.Fatal("unsupported closure ran managed receiver")
	}
}
