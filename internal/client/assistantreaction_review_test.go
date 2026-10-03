package client

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// The exact key a request was sealed to is the only one whose assistant
// reaction attributes to it; a request whose key was never recorded proves
// nothing; a reaction never goes to a replacement of the requester's key.
func TestAssistantReactionExactKeys(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	progressReader(t, w.alice, w.bob)
	withAgentReaction(t, w.alice, w.bob)
	ask := func(body string) string {
		q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return q.ID
	}
	q1, q2 := ask("first"), ask("second")
	eventually(t, "requests at bob", func() bool { return inboxCount(t, w.bob, "id IN (?, ?)", q1, q2) == 2 })
	var sealed string
	w.alice.store.db.QueryRow(`SELECT coalesce(recipient_fp,'') FROM outbox WHERE id=?`, q1).Scan(&sealed)
	if sealed != w.bob.Self().Fingerprint() {
		t.Fatalf("request key not captured: %q", sealed)
	}
	def := "assistant:" + w.bob.Address + "/default"
	if _, err := w.bob.reactAsAssistant(tctx(t), q1, "claude", "👍", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "attributed on the exact key", func() bool { return slices.Equal(reactorIDs(v1Reactors(t, w.alice, q1, "👍")), []string{def}) })

	// A request stored without its key (older rows) proves no attribution.
	w.alice.store.db.Exec(`UPDATE outbox SET recipient_fp=NULL WHERE id=?`, q2)
	legacy, err := w.bob.reactAsAssistant(tctx(t), q2, "claude", "👍", false)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "unprovable reaction held", func() bool {
		return inboxCount(t, w.alice, "id = ?", legacy.ID) == 0 && quarantined(t, w.alice, legacy.ID)
	})

	// Queued for the old requester key, the copy is not sent once it changes.
	f := injectFaults(w.bob)
	f.addAfter("GET", "/profile", 1, 1, false) // the send-time control check passes; delivery's capability read is lost
	queued, err := w.bob.reactAsAssistant(tctx(t), q1, "claude", "🎉", false)
	if err != nil {
		t.Fatal(err)
	}
	if s, _, _, _ := w.bob.store.outboxState(queued.ID); s != stateQueued {
		t.Fatalf("fixture: reaction not queued: %s", s)
	}
	newAlice, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.store.pin(newAlice.Public(w.alice.Address)); err != nil { // trusted, as after `trust`
		t.Fatal(err)
	}
	f.add("GET", "/v1/agents/admin/alice", 1000, false) // directory out of reach: delivery uses the pinned key
	w.bob.FlushOutbox(tctx(t))
	if s, _, _, _ := w.bob.store.outboxState(queued.ID); s != stateNotDelivered {
		t.Fatalf("reaction for an old request sent to a replacement key: %s", s)
	}
	before := count(t, w.bob, "outbox")
	if _, err = w.bob.reactAsAssistant(tctx(t), q1, "claude", "✅", false); err == nil || !strings.Contains(err.Error(), "requester's key changed") {
		t.Fatalf("reaction to a replaced requester key: %v", err)
	}
	if count(t, w.bob, "outbox") != before {
		t.Fatal("refused reaction queued")
	}

	// A trusted replacement of the executor's key cannot react to the old request.
	newBob, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.pin(newBob.Public(w.bob.Address)); err != nil {
		t.Fatal(err)
	}
	recipient, _ := w.alice.Self().Recipient()
	in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
		Sub: envelope.SubReaction, Body: `{"emoji":"🔥","op":"add","n":50}`, Ref: &envelope.Ref{ID: q1, Fingerprint: w.alice.Self().Fingerprint()}, Origin: envelope.OriginAgentPrefix + "claude"}
	env, err := envelope.Seal(in, newBob.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.accept(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if inboxCount(t, w.alice, "id = ?", in.ID) != 0 || len(v1Reactors(t, w.alice, q1, "🔥")) != 0 {
		t.Fatal("replacement key attributed to the old request's executor")
	}
}

func quarantined(t *testing.T, a *Agent, id string) bool {
	var n int
	a.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, id).Scan(&n)
	return n == 1
}

// A member-hosted named assistant in a DM reacts as itself beside its
// owner's own mark; its owner's device shows its catalog label, the other
// member a host+id fallback with host, agent and PID for a catalog lookup.
func TestAssistantReactionMemberHostedDMAndLabels(t *testing.T) {
	bin := reactHarness(t, "reactmember", `ok\nreaction: 🎉\nemotion: calm\n`)
	w, conv, _, _ := agentWorld(t)
	waitNamedAgentCaps(t, w.bob)
	withAgentReaction(t, w.alice, w.bob)
	_ = bin
	one, err := w.bob.CreateLocalAgent("Bob Reviewer", Responder{Harness: "reactmember", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	two, err := w.bob.CreateLocalAgent("Bob Reviewer", Responder{Harness: "reactmember", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	ask := func(record protocol.AgentRecord) (ParticipationInfo, ConvSent) {
		p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.bob.Address, record.ID, nil, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "invite at bob", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
		if _, err = w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
			t.Fatal(err)
		}
		eventually(t, "active", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
		q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "react please")
		if err != nil {
			t.Fatal(err)
		}
		replyAt(t, w.alice, conv, q.ID)
		return p, q
	}
	p1, q1 := ask(one)
	p2, q2 := ask(two)
	if _, err = w.bob.React(tctx(t), ControlRef{Conv: conv, ID: q1.LID, Fingerprint: w.alice.Self().Fingerprint()}, "🎉", false); err != nil {
		t.Fatal(err)
	}
	bobPerson, _, _ := w.bob.Person()
	want := []string{"assistant:" + p1.PID, bobPerson.Person}
	slices.Sort(want)
	var r1, r2 Reactor
	eventually(t, "owner and member-hosted assistant distinct at alice", func() bool {
		msgs, _ := w.alice.ConversationMessages(conv)
		rs1, rs2 := reactorsOn(msgs, q1.LID, "🎉"), reactorsOn(msgs, q2.LID, "🎉")
		for _, r := range rs1 {
			if r.Assistant {
				r1 = r
			}
		}
		if len(rs2) == 1 {
			r2 = rs2[0]
		}
		return slices.Equal(reactorIDs(rs1), want) && len(rs2) == 1
	})
	if r1.Label == r2.Label || r1.AgentID != one.ID || r1.PID != p1.PID || r1.Host != w.bob.Address || !strings.Contains(r1.Label, one.ID[:8]) {
		t.Fatalf("two assistants on one host not distinct: %+v %+v", r1, r2)
	}
	eventually(t, "own catalog label at the host", func() bool {
		msgs, _ := w.bob.ConversationMessages(conv)
		for _, r := range reactorsOn(msgs, q2.LID, "🎉") {
			if r.Assistant && r.PID == p2.PID {
				return r.Label == "Bob Reviewer"
			}
		}
		return false
	})
}

// A member's late-linked device recovers an outside assistant's reaction
// through history, and later its removal.
func TestAssistantReactionLinkedDeviceRecovery(t *testing.T) {
	w, host, conv, _, _, records, _ := externalAgentWorld(t)
	withAgentReaction(t, w.alice, w.bob, host)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invite at host", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "active", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "react later")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, q.ID)
	if _, err = host.reactAsAssistant(tctx(t), q.ID, "agentstub", "🎉", false); err != nil {
		t.Fatal(err)
	}
	has := func(a *Agent) bool {
		msgs, _ := a.ConversationMessages(conv)
		rs := reactorsOn(msgs, q.LID, "🎉")
		return len(rs) == 1 && rs[0].ID == "assistant:"+p.PID
	}
	eventually(t, "reaction at alice", func() bool { return has(w.alice) })

	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	req := pendingLink(t, w.alice)
	if err = w.alice.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	fakeNotify(phone)
	waitNamedAgentCaps(t, phone)
	withAgentReaction(t, phone)
	w.alice.convWork.due(convRetry | convHistory)
	w.alice.kickNow()
	eventually(t, "late device recovers the assistant reaction", func() bool { return has(phone) })
	if _, err = host.reactAsAssistant(tctx(t), q.ID, "agentstub", "🎉", true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "removal reaches the late device too", func() bool {
		msgs, _ := phone.ConversationMessages(conv)
		return len(reactorsOn(msgs, q.LID, "🎉")) == 0 && !has(w.alice)
	})
}

// A group agent hosted on a member device reacts as itself to the request
// addressed to it.
func TestAssistantReactionMemberHostedGroup(t *testing.T) {
	stub := installAgentStub(t)
	stub.mode("sleep")
	w, producer, packet, stops := groupTurnsFixture(t)
	var host *Agent
	members := []*Agent{}
	for a := range stops {
		members = append(members, a)
		if a != producer && host == nil {
			host = a
		}
	}
	for _, a := range members {
		addCapSuccessor(t, a, protocol.CapAgentReaction)
	}
	record, err := host.CreateLocalAgent("Member reactor", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	p, err := producer.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member host invitation", func() bool { v, e := host.Participation(p.PID); return e == nil && v.State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "active", func() bool { v, e := producer.Participation(p.PID); return e == nil && v.Claimable() })
	q, err := producer.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "member group react")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member host runs", func() bool { _, err := os.Stat(stub.log + ".started"); return err == nil })
	if _, err = host.reactAsAssistant(tctx(t), q.ID, "agentstub", "👀", false); err != nil {
		t.Fatal(err)
	}
	_ = w
	eventually(t, "member-hosted group assistant reaction at producer", func() bool {
		msgs, _ := producer.ConversationMessages(conv)
		rs := reactorsOn(msgs, q.LID, "👀")
		return len(rs) == 1 && rs[0].ID == "assistant:"+p.PID && rs[0].Assistant
	})
}

// A group member's device linked after an assistant reaction gets the
// group's history; the reaction never stalls it.
func TestAssistantReactionGroupLinkedHistory(t *testing.T) {
	stub := installAgentStub(t)
	stub.mode("sleep")
	w, producer, packet, stops := groupTurnsFixture(t)
	for a := range stops {
		addCapSuccessor(t, a, protocol.CapAgentReaction)
	}
	host := w.bob
	record, err := host.CreateLocalAgent("Member reactor", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	conv := packet.State.Conv
	p, err := producer.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member host invitation", func() bool { v, e := host.Participation(p.PID); return e == nil && v.State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "active", func() bool { v, e := producer.Participation(p.PID); return e == nil && v.Claimable() })
	q, err := producer.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "linked group react")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "member host runs", func() bool { _, err := os.Stat(stub.log + ".started"); return err == nil })
	if _, err = host.reactAsAssistant(tctx(t), q.ID, "agentstub", "👀", false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reaction at producer", func() bool {
		msgs, _ := producer.ConversationMessages(conv)
		return len(reactorsOn(msgs, q.LID, "👀")) == 1
	})
	// Stored only with the receiving device's own exact live admission.
	own, err := groupMemberAdmission(producer.store.db, packet, producer.Address, producer.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	var stamps []string
	rows, err := producer.store.db.Query(`SELECT coalesce(group_admission,'') FROM inbox WHERE conv=? AND sub=? AND pid=?`, conv, envelope.SubReaction, p.PID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		stamps = append(stamps, s)
	}
	rows.Close()
	if len(stamps) != 1 || stamps[0] != own.Hash() {
		t.Fatalf("group assistant reaction stamps %q, want own admission %s", stamps, own.Hash())
	}
	plain, err := producer.SendConv(tctx(t), conv, ConvOutgoing{Body: "plain after reaction"})
	if err != nil {
		t.Fatal(err)
	}
	_ = plain
	phone, await, _ := linkPhone(t, producer, "phone")
	request := pendingLink(t, producer)
	if err = producer.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-await; result.err != nil {
		t.Fatal(result.err)
	}
	runAgent(t, phone)
	publishGroupFixtureCaps(t, phone, true)
	dropCapSuccessor(t, phone, protocol.CapAgentReaction) // an older reader: no agr1 yet
	copies, err := w.alice.groupDeliveryCopies(tctx(t), packet)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.addConvOutbox(copies, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "linked verified context", func() bool { _, e := phone.GroupContext(conv); return e == nil })
	if _, err = producer.historyPageFor(phone.Self(), historyPos{}); err != nil {
		t.Fatalf("group history page with an assistant reaction: %v", err)
	}
	eventually(t, "linked group history converges", func() bool {
		for _, m := range groupTurns(t, phone, conv) {
			if m.Body == "plain after reaction" {
				return true
			}
		}
		return false
	})
	// The reaction's copy waits for agr1, keeping its group requirement,
	// across a restart of its producer; agr1 signed releases it.
	eventually(t, "reaction history waits for agr1", func() bool {
		s, r := historyReactionCopy(t, producer, phone.Address)
		return s == stateConvWaiting && r == protocol.CapGroup
	})
	if len(reactorsOn(groupTurns(t, phone, conv), q.LID, "👀")) != 0 {
		t.Fatal("assistant reaction reached a reader without agr1")
	}
	producer = reopen(t, producer, stops[producer])
	stillWaiting(t, producer, phone.Address, protocol.CapGroup)
	addCapSuccessor(t, phone, protocol.CapAgentReaction)
	eventually(t, "the assistant's reaction recovered on the linked device", func() bool {
		rs := reactorsOn(groupTurns(t, phone, conv), q.LID, "👀")
		return len(rs) == 1 && rs[0].ID == "assistant:"+p.PID && rs[0].Assistant
	})

	// A proof not held yet waits (reasonProof); only a proven mismatch is
	// left out (reasonInvalid).
	orig := envelope.Inner{V: envelope.Version3, Conv: conv, PID: p.PID, AgentID: record.ID, Kind: envelope.KindMessage, Sub: envelope.SubReaction,
		Ref: &envelope.Ref{ID: q.ID, Fingerprint: producer.Self().Fingerprint()}}
	bound := func(q dbq, from, fp string) (string, error) {
		return assistantHistoryCheck(q, orig, from, fp, producer.Address, producer.Self().Fingerprint())
	}
	if reason, err := bound(producer.store.db, host.Address, host.Self().Fingerprint()); err != nil {
		t.Fatalf("exact binding refused: %s %v", reason, err)
	}
	if reason, err := bound(producer.store.db, w.alice.Address, w.alice.Self().Fingerprint()); reason != reasonInvalid || err == nil {
		t.Fatalf("another host's key: %q %v", reason, err)
	}
	tx, err := producer.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM participation_events WHERE pid=?`, p.PID); err != nil {
		t.Fatal(err)
	}
	if reason, err := bound(tx, host.Address, host.Self().Fingerprint()); reason != reasonProof || err == nil {
		t.Fatalf("missing participation proof: %q %v", reason, err)
	}
}

// historyReactionCopy is a's history copy of an assistant reaction for the
// device dev: its state and stored requirement.
func historyReactionCopy(t *testing.T, a *Agent, dev string) (state, required string) {
	t.Helper()
	a.store.db.QueryRow(`SELECT state, coalesce(required_cap,'') FROM outbox WHERE recipient=? AND sub=? AND json_extract(CASE WHEN json_valid(body) THEN body ELSE '{}' END,'$.sub')=?`,
		dev, envelope.SubHistory, envelope.SubReaction).Scan(&state, &required)
	return state, required
}

// stillWaiting checks, after a restart's own release and flush, that a's
// assistant reaction history copy for dev still waits with required.
func stillWaiting(t *testing.T, a *Agent, dev, required string) {
	t.Helper()
	feats, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	a.releaseConv(tctx(t), feats)
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if s, r := historyReactionCopy(t, a, dev); s != stateConvWaiting || r != required {
		t.Fatalf("after restart the reaction history copy is %s (%s), not waiting (%s)", s, r, required)
	}
}

// reopen restarts a's daemon on its own home: stopped, closed, opened, run.
func reopen(t *testing.T, a *Agent, stop func()) *Agent {
	t.Helper()
	stop()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	fakeNotify(b)
	runAgent(t, b)
	return b
}

// An outside host's assistant reaction carried as DM history to a member's
// later device needs agr1 there besides apx1: it waits for an older reader,
// across the member's restart, and is delivered once agr1 is signed.
func TestAssistantReactionExternalHistoryWaitsForOldReader(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _, stopBob := agentWorld(t)
	host := mustJoin(t, filepath.Join(t.TempDir(), "charlie"), w.aliceInvites("charlie"), "host")
	runAgent(t, host)
	persons(t, host)
	for _, a := range []*Agent{w.alice, w.bob, host} {
		fakeNotify(a)
		waitNamedAgentCaps(t, a)
	}
	record, err := host.CreateLocalAgent("Reviewer", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	withAgentReaction(t, w.alice, w.bob, host)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, record.ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "invite at host", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err = host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "active", func() bool { return stateAt(t, w.bob, p.PID).Claimable() })
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "react later")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, q.ID)
	if _, err = host.reactAsAssistant(tctx(t), q.ID, "agentstub", "🎉", false); err != nil {
		t.Fatal(err)
	}
	has := func(a *Agent) bool {
		msgs, _ := a.ConversationMessages(conv)
		rs := reactorsOn(msgs, q.LID, "🎉")
		return len(rs) == 1 && rs[0].ID == "assistant:"+p.PID && rs[0].Assistant
	}
	eventually(t, "reaction at bob", func() bool { return has(w.bob) })

	phone, awaited, _ := linkPhone(t, w.bob, "phone")
	req := pendingLink(t, w.bob)
	if err = w.bob.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	fakeNotify(phone)
	waitNamedAgentCaps(t, phone)
	dropCapSuccessor(t, phone, protocol.CapAgentReaction) // an older reader: participation caps, no agr1
	w.bob.convWork.due(convRetry | convHistory)
	w.bob.kickNow()
	eventually(t, "the request reaches the later device as history", func() bool {
		msgs, _ := phone.ConversationMessages(conv)
		return slices.ContainsFunc(msgs, func(m ConvMessage) bool { return m.LID == q.LID })
	})
	eventually(t, "reaction history waits for agr1", func() bool {
		s, r := historyReactionCopy(t, w.bob, phone.Address)
		return s == stateConvWaiting && r == protocol.CapExternalParticipation
	})
	if has(phone) {
		t.Fatal("assistant reaction reached a reader without agr1")
	}
	bob := reopen(t, w.bob, stopBob)
	stillWaiting(t, bob, phone.Address, protocol.CapExternalParticipation)
	withAgentReaction(t, phone)
	eventually(t, "released and delivered once agr1 is signed", func() bool { return has(phone) })
}
