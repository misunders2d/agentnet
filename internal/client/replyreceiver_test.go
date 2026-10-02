package client

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func receiverBindings(t *testing.T, a *Agent) []ReplyReceiverBinding {
	t.Helper()
	rows, err := a.ReplyReceiverBindings()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}
func receiverDirect(t *testing.T, from, to *Agent, in envelope.Inner) envelope.Envelope {
	t.Helper()
	in.From, in.To = from.Address, to.Address
	if in.ID == "" {
		in.ID = protocol.NewID()
	}
	key, err := to.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Seal(in, from.id.Sign, key)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestReplyReceiverSelectionRestartAndNoFallback(t *testing.T) {
	stub := installAgentStub(t)
	w := newWorld(t, "")
	local, err := w.alice.CreateLocalAgent("return assistant", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	defaultBefore, _ := w.alice.Responder()
	owner, _ := nativeReceiverFixture(t, w.alice, "pi")
	selected := []ReplyReceiver{{Kind: "human"}, {Kind: "managed_agent", AgentID: local.ID, Instructions: "continue the authorized local plan", Mode: envelope.KindQuestion}, {Kind: "live_session", SessionHandle: owner.Handle}}
	for _, receiver := range selected {
		want := receiver
		sent, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "request", ReplyReceiver: &receiver})
		if e != nil {
			t.Fatal(e)
		}
		receiver.Kind = "human"
		receiver.AgentID = ""
		receiver.SessionHandle = ""
		var raw string
		if e = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, sent.ID).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var sealed envelope.Envelope
		json.Unmarshal([]byte(raw), &sealed)
		inner, e := envelope.Open(sealed, w.bob.id, w.bob.Address, w.alice.Self())
		if e != nil {
			t.Fatal(e)
		}
		plaintext, _ := json.Marshal(inner)
		if strings.Contains(string(plaintext), "reply_receiver") || strings.Contains(string(plaintext), owner.Handle) {
			t.Fatal("local receiver leaked to wire")
		}
		reply := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindAnswer, Body: "answer", ReplyTo: sent.ID})
		for range 2 {
			if e = w.alice.verifyAndStore(tctx(t), reply); e != nil {
				t.Fatal(e)
			}
		}
		rows := receiverBindings(t, w.alice)
		found := false
		for _, b := range rows {
			if b.RequestRef == sent.ID {
				found = true
				want.Preset = b.Receiver.Preset // native-derived, never caller-selected
				if want.Kind == "managed_agent" && want.Preset == "" {
					t.Fatal("missing frozen native preset")
				}
				if b.Receiver != want || b.State != "pending" || len(b.Inputs) != 1 || b.Inputs[0].State != "pending" {
					t.Fatalf("binding %+v", b)
				}
				if want.Kind == "managed_agent" && (b.Executor == nil || b.Executor.AgentID != local.ID) {
					t.Fatal("missing selected executor snapshot")
				}
				if state, _ := w.alice.store.jobState(reply.ID); state != "" {
					t.Fatalf("receiver started default job: %q", state)
				}
			}
		}
		if !found {
			t.Fatal("binding missing")
		}
	}
	if stub.runs() != 0 {
		t.Fatal("receiver executed")
	}
	defaultAfter, _ := w.alice.Responder()
	if !reflect.DeepEqual(defaultBefore, defaultAfter) {
		t.Fatal("default altered")
	}
	home := w.alice.home
	w.alice.Close()
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if len(receiverBindings(t, reopened)) != 3 {
		t.Fatal("restart lost bindings")
	}
	if err = reopened.SetLocalAgentResponder(local.ID, nil); err != nil {
		t.Fatal(err)
	}
	for _, b := range receiverBindings(t, reopened) {
		if b.Receiver.AgentID == local.ID && (b.State != "refused" || b.Executor.AgentID != local.ID || len(b.Inputs) != 1) {
			t.Fatal("disabled receiver fell back")
		}
	}
	for _, invalid := range []ReplyReceiver{{Kind: "managed_agent", AgentID: local.ID}, {Kind: "managed_agent", AgentID: protocol.NewID()}, {Kind: "live_session", SessionHandle: "/private/native/path"}, {Kind: "human", AgentID: local.ID}} {
		if _, err = reopened.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "invalid receiver", ReplyReceiver: &invalid}); err == nil {
			t.Fatal("invalid receiver allowed")
		}
	}
}

func TestReplyReceiverAtomicRollbackAndLegacyMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	last := -1
	for i, step := range schema {
		if step == replyReceiverSchema {
			last = i
		}
	}
	if last < 0 {
		t.Fatal("receiver append-only step missing")
	}
	old, err := sqlitedb.Open(path, schema[:last])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = old.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,follow_up) VALUES('old','bob/laptop','exact old body','exact signed envelope','queued',1,'legacy instruction')`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	current, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer current.db.Close()
	var body, raw, follow string
	var binding sql.NullString
	if err = current.db.QueryRow(`SELECT body,envelope,follow_up,reply_receiver FROM outbox WHERE id='old'`).Scan(&body, &raw, &follow, &binding); err != nil || body != "exact old body" || raw != "exact signed envelope" || follow != "legacy instruction" || binding.Valid {
		t.Fatalf("migration changed legacy %q %q %v", body, raw, err)
	}
	w, conv, _ := dmFiles(t)
	if _, err = w.alice.store.db.Exec(`CREATE TRIGGER reject_receiver BEFORE INSERT ON reply_receivers BEGIN SELECT RAISE(ABORT,'injected binding failure'); END`); err != nil {
		t.Fatal(err)
	}
	r := ReplyReceiver{Kind: "human"}
	if _, err = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "must rollback direct", ReplyReceiver: &r}); err == nil {
		t.Fatal("direct binding failure accepted")
	}
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "must rollback DM", ReplyReceiver: &r}); err == nil {
		t.Fatal("DM binding failure accepted")
	}
	var count int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE body LIKE 'must rollback%'`).Scan(&count)
	if count != 0 {
		t.Fatal("deliverable unbound requests survived rollback")
	}
	if len(receiverBindings(t, w.alice)) != 0 {
		t.Fatal("orphan obligation")
	}
	if _, err = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "legacy", FollowUp: "summary", ReplyReceiver: &r}); err == nil {
		t.Fatal("selected receiver mixed with legacy summary")
	}
	// The exact original envelope bytes remain unchanged by local metadata.
	w.alice.store.db.Exec(`DROP TRIGGER reject_receiver`)
	in := envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, Kind: envelope.KindMessage, Body: "frozen wire"}
	env := receiverDirect(t, w.alice, w.bob, in)
	encoded, _ := json.Marshal(env)
	b, e := w.alice.prepareReplyReceiver(&r)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.store.addOutbox(env, in, "", nil, boundOutgoing{binding: b, fingerprint: w.bob.Self().Fingerprint()}); e != nil {
		t.Fatal(e)
	}
	w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&raw)
	if raw != string(encoded) {
		t.Fatal("local binding rewrote signed bytes")
	}
	stub := installAgentStub(t)
	rec, e := w.alice.CreateLocalAgent("tx receiver", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	managed := ReplyReceiver{Kind: "managed_agent", AgentID: rec.ID, Instructions: "local plan", Mode: envelope.KindQuestion}
	beforeOutbox = func() {
		if e = w.alice.SetLocalAgentResponder(rec.ID, nil); e != nil {
			t.Fatal(e)
		}
	}
	defer func() { beforeOutbox = func() {} }()
	_, e = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "disabled between seal and tx", ReplyReceiver: &managed})
	beforeOutbox = func() {}
	if e == nil {
		t.Fatal("stale local receiver validation")
	}
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE body='disabled between seal and tx'`).Scan(&count)
	if count != 0 {
		t.Fatal("disabled receiver request installed")
	}
}

func TestReplyReceiverHumanDMCurrentLinkedPersonAndCancellation(t *testing.T) {
	w, conv, _ := dmFiles(t)
	r := ReplyReceiver{Kind: "human"}
	request, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "human consultation", ReplyReceiver: &r})
	if err != nil {
		t.Fatal(err)
	}
	phone, await, _ := linkPhone(t, w.bob, "return-phone")
	pending := pendingLink(t, w.bob)
	if err = w.bob.DecideLink(tctx(t), pending.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	eventually(t, "new linked person request", func() bool { return inboxCount(t, phone, `conv=? AND lid=?`, conv, request.LID) == 1 })
	reply, err := phone.SendConv(tctx(t), conv, ConvOutgoing{Body: "human reply from linked device", ReplyTo: request.ID})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "person-bound input", func() bool { rows := receiverBindings(t, w.alice); return len(rows) == 1 && len(rows[0].Inputs) == 1 })
	b := receiverBindings(t, w.alice)[0]
	if b.RequestRef != request.LID || b.State != "pending" {
		t.Fatalf("wrong logical obligation %+v", b)
	}
	_, raw := rootOf(t, w.alice, conv)
	for _, mode := range []string{"unknown", "pid", "replica"} {
		in := envelope.Inner{Kind: envelope.KindMessage, Body: mode, Conv: conv, LID: protocol.NewID(), Root: raw, ReplyTo: request.ID}
		switch mode {
		case "unknown":
			in.ReplyTo = protocol.NewID()
		case "pid":
			in.Kind = envelope.KindAnswer
			in.PID = protocol.NewID()
		case "replica":
			in.Replica = true
		}
		if err = w.alice.verifyAndStore(tctx(t), craft(t, phone, w.alice, in)); err != nil {
			t.Fatal(err)
		}
	}
	if len(receiverBindings(t, w.alice)[0].Inputs) != 1 {
		t.Fatal("unbound or wrong-PID input routed")
	}
	// An incoming logical identity never substitutes for the original outgoing
	// request, and the same identity in another home has no binding there.
	incoming := envelope.Inner{ID: protocol.NewID(), Kind: envelope.KindMessage, Body: "opposite direction", Conv: conv, LID: request.LID, Root: raw}
	if err = w.alice.verifyAndStore(tctx(t), craft(t, phone, w.alice, incoming)); err != nil {
		t.Fatal(err)
	}
	oppositeReply := envelope.Inner{Kind: envelope.KindMessage, Body: "reply to incoming physical ID", Conv: conv, LID: protocol.NewID(), Root: raw, ReplyTo: incoming.ID}
	if err = w.alice.verifyAndStore(tctx(t), craft(t, phone, w.alice, oppositeReply)); err != nil {
		t.Fatal(err)
	}
	if len(receiverBindings(t, w.alice)[0].Inputs) != 1 {
		t.Fatal("opposite-direction LID substituted for outgoing request")
	}
	other, err := openStore(filepath.Join(t.TempDir(), "other-home.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.db.Close()
	if err = other.addInbox(envelope.Inner{ID: protocol.NewID(), From: phone.Address, Kind: envelope.KindMessage, ReplyTo: request.ID}, phone.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	var n int
	other.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs`).Scan(&n)
	if n != 0 {
		t.Fatal("cross-home binding")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE reply_receivers SET canceled_at=1 WHERE id=?`, b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.SendConv(tctx(t), conv, ConvOutgoing{Body: "late canceled response", ReplyTo: request.ID}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "canceled response remains visible", func() bool { return inboxCount(t, w.alice, `conv=? AND body='late canceled response'`, conv) == 1 })
	if got := receiverBindings(t, w.alice)[0]; got.State != "canceled" || len(got.Inputs) != 2 {
		t.Fatalf("canceled work revived %+v", got)
	}
	if reply.LID == "" {
		t.Fatal("reply logical identity absent")
	}
}

func TestReplyReceiverExternalExactRequestPIDAndFanCopies(t *testing.T) {
	w, host, conv, _, stub, records, stopHost := externalAgentWorld(t)
	var participations []ParticipationInfo
	for _, record := range records {
		p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "bounded binding fixture")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "external invitation", func() bool { got, e := host.Participation(p.PID); return e == nil && got.State == PartInvited })
		if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
		eventually(t, "external acceptance", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
		participations = append(participations, p)
	}
	stopHost() // no execution: inputs are exercised through real signed receive
	local, err := w.alice.CreateLocalAgent("selected return", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	receiver := ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "local plan", Mode: envelope.KindQuestion}
	var requests []ConvSent
	for i, p := range participations {
		sent, e := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "question", PID: p.PID, Target: &envelope.Target{Address: host.Address, Fingerprint: host.Self().Fingerprint(), AgentID: records[i].ID}, ReplyReceiver: &receiver})
		if e != nil {
			t.Fatal(e)
		}
		requests = append(requests, sent)
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND lid=? AND reply_receiver IS NOT NULL`, conv, sent.LID).Scan(&n)
		if n != 1 || len(sent.Copies) != 2 {
			t.Fatalf("named binding per fan copy: linked=%d copies=%d", n, len(sent.Copies))
		}
	}
	_, raw := rootOf(t, w.alice, conv)
	if err = w.alice.SetLocalAgentResponder(local.ID, nil); err != nil {
		t.Fatal(err)
	} // retain input/correlation, no dispatcher execution in this foundation regression
	wrong := envelope.Inner{Kind: envelope.KindAnswer, Body: "wrong logical request", Conv: conv, LID: protocol.NewID(), Root: raw, PID: participations[1].PID, AgentID: records[1].ID, ReplyTo: requests[0].ID}
	if err = w.alice.verifyAndStore(tctx(t), craft(t, host, w.alice, wrong)); err != nil {
		t.Fatal(err)
	}
	for _, b := range receiverBindings(t, w.alice) {
		if len(b.Inputs) != 0 {
			t.Fatal("wrong PID/agent bound")
		}
	}
	for i, request := range requests {
		in := envelope.Inner{Kind: envelope.KindAnswer, Body: "correct answer", Conv: conv, LID: protocol.NewID(), Root: raw, PID: participations[i].PID, AgentID: records[i].ID, ReplyTo: request.ID}
		env := craft(t, host, w.alice, in)
		for range 2 {
			if err = w.alice.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
		}
		in.ID = protocol.NewID() // same logical reply re-encrypted as another physical copy
		if err = w.alice.verifyAndStore(tctx(t), craft(t, host, w.alice, in)); err != nil {
			t.Fatal(err)
		}
	}
	rows := receiverBindings(t, w.alice)
	if len(rows) != 2 {
		t.Fatal("logical obligations duplicated")
	}
	for _, b := range rows {
		if len(b.Inputs) != 1 || b.State != "refused" || b.Receiver.AgentID != local.ID {
			t.Fatalf("external binding %+v", b)
		}
	}
	if stub.runs() != 0 {
		t.Fatal("receiver fixture executed")
	}
}

func TestReplyReceiverNamedAuthorityAndClarificationData(t *testing.T) {
	stub := installAgentStub(t)
	w := newWorld(t, "")
	stop := runAgent(t, w.bob)
	waitNamedAgentCaps(t, w.bob)
	stop()
	local, err := w.alice.CreateLocalAgent("chosen return", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	remote, err := w.bob.CreateLocalAgent("remote executor", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	r := ReplyReceiver{Kind: "managed_agent", AgentID: local.ID, Instructions: "local plan", Mode: envelope.KindQuestion}
	request, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "request", Target: &envelope.Target{Address: w.bob.Address, Fingerprint: w.bob.Self().Fingerprint(), AgentID: remote.ID}, ReplyReceiver: &r})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"agent", "unknown", "key"} {
		in := envelope.Inner{Kind: envelope.KindResult, ReplyTo: request.ID, AgentID: remote.ID, Body: "wrong"}
		from := w.bob
		switch mode {
		case "agent":
			in.AgentID = protocol.NewID()
		case "unknown":
			in.ReplyTo = protocol.NewID()
		case "key":
			id, e := identity.Generate()
			if e != nil {
				t.Fatal(e)
			}
			from = &Agent{Address: w.bob.Address, id: id}
		}
		if err = w.alice.verifyAndStore(tctx(t), receiverDirect(t, from, w.alice, in)); err != nil {
			t.Fatal(err)
		}
	}
	if len(receiverBindings(t, w.alice)[0].Inputs) != 0 {
		t.Fatal("wrong authority routed")
	}
	clarification := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindQuestion, Body: "which approved scope?", ReplyTo: request.ID})
	if err = w.alice.verifyAndStore(tctx(t), clarification); err != nil {
		t.Fatal(err)
	}
	if state, _ := w.alice.store.jobState(clarification.ID); state != stateHeld {
		t.Fatalf("clarification granted remote task %q", state)
	}
	answer := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindResult, Body: "terminal result", ReplyTo: request.ID, AgentID: remote.ID})
	for range 2 {
		if err = w.alice.verifyAndStore(tctx(t), answer); err != nil {
			t.Fatal(err)
		}
	}
	b := receiverBindings(t, w.alice)[0]
	if len(b.Inputs) != 2 || b.Receiver.AgentID != local.ID || b.Executor.AgentID != local.ID || b.State != "pending" {
		t.Fatalf("named correlation %+v", b)
	}
	if stub.runs() != 0 {
		t.Fatal("binding executed model")
	}
}
