package client

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// needsYou is the page review's conversation item for pid (and, if id is
// given, that request), if listed.
func needsYou(t *testing.T, a *Agent, pid, id string) (ConvReview, bool) {
	t.Helper()
	p, err := a.PageReview()
	if err != nil {
		t.Fatalf("page review: %v", err)
	}
	for _, r := range p.Conv {
		if r.PID == pid && (id == "" || r.ID == id) {
			return r, true
		}
	}
	return ConvReview{}, false
}

// inviteClaiming signs and sends an invitation for host's agent into conv
// as inviter, a member, with ts as its claimed time: what InviteAgent
// does, with any time a member may write.
func inviteClaiming(t *testing.T, inviter *Agent, conv, host string, ts int64) string {
	t.Helper()
	m, err := inviter.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	me, ok, err := inviter.store.selfPerson(inviter.Address)
	if err != nil || !ok {
		t.Fatalf("inviter person: %v", err)
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: ts,
		Author:   protocol.EventAuthor{Person: me.info.Person, Roster: me.info.Roster, Address: inviter.Address, Fingerprint: me.info.Fingerprint},
		Audience: protocol.AudienceConversation, Note: "claimed time"}
	for _, p := range m.persons {
		if d, ok := p.device(host); ok {
			ev.Host = &protocol.ParticipationHost{Person: p.info.Person, Address: d.Address, Fingerprint: d.Fingerprint()}
		}
	}
	if ev.Host == nil {
		t.Fatalf("%s is not a member device of %s", host, conv)
	}
	if err := inviter.recordAndSend(tctx(t), ev); err != nil {
		t.Fatal(err)
	}
	return ev.PID
}

// reaching sends plain messages from a into conv until its copy to device
// to is not kept waiting (to's capabilities, as the Hub lists them now,
// let it take part), then until to holds that message: whatever a sends
// next goes to that device directly.
func reaching(t *testing.T, a *Agent, conv string, to *Agent) {
	t.Helper()
	var body string
	for i, deadline := 0, time.Now().Add(15*time.Second); ; i++ {
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached %s directly", a.Address, to.Address)
		}
		body = "reach " + to.Address + " " + strings.Repeat(".", i)
		sent, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: body})
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.IndexFunc(sent.Copies, func(c ConvCopy) bool { return c.To == to.Address }); i >= 0 && sent.Copies[i].State != stateConvWaiting {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	eventually(t, to.Address+" to hold "+body, func() bool {
		msgs, _ := to.ConversationMessages(conv)
		return slices.ContainsFunc(msgs, func(m ConvMessage) bool { return m.Body == body })
	})
}

// An invitation's time on the page is when it reached this device, never
// the inviter's claim: one a member signs far in the future (past year
// 9999) is listed at its arrival, and the page review still encodes.
func TestPageReviewInviteAtIsArrival(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	before := time.Now().Add(-time.Second)
	pid := inviteClaiming(t, w.bob, conv, w.alice.Address, 1e12)
	eventually(t, "the invitation at alice", func() bool { _, ok := needsYou(t, w.alice, pid, ""); return ok })
	r, _ := needsYou(t, w.alice, pid, "")
	if r.Reason != ReviewInvite || r.At.Before(before) || r.At.After(time.Now().Add(time.Second)) {
		t.Fatalf("the invitation's time is the inviter's claim: %+v", r)
	}
	p, err := w.alice.PageReview()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := json.Marshal(p); err != nil {
		t.Fatalf("the page review no longer encodes: %v", err)
	}
}

// One participation that cannot be resolved here does not take the page
// review with it: it is left out, and the other invitations are listed.
func TestPageReviewSkipsUnresolvableParticipation(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	inv, err := w.bob.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "help")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the invitation at alice", func() bool { _, ok := needsYou(t, w.alice, inv.PID, ""); return ok })
	// A record naming this host in a conversation not held here: resolving
	// it fails ("no conversation … here").
	bad := protocol.NewID()
	if _, err := w.alice.store.db.Exec(`INSERT INTO participation_events(hash, conv, pid, type, author, event, received_at, prev) VALUES(?, ?, ?, ?, ?, ?, 0, '')`,
		protocol.NewID(), strings.Repeat("e", 64), bad, protocol.EventInvite, w.bob.Self().Fingerprint(), `{"host":{"address":"`+w.alice.Address+`"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.participation(strings.Repeat("e", 64), bad); err == nil {
		t.Fatal("setup: the broken participation resolves")
	}
	p, err := w.alice.PageReview()
	if err != nil {
		t.Fatalf("one participation failed the page review: %v", err)
	}
	if len(p.Conv) != 1 || p.Conv[0].PID != inv.PID {
		t.Fatalf("page review: %+v", p.Conv)
	}
}

// Held person turns: a question this person asked from another of their
// devices is theirs, not held for them (its copy here is "out"); one in a
// conversation deleted here is gone with it. The other person's question
// is listed until then.
func TestPageReviewHeldTurns(t *testing.T) {
	t.Parallel()
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.alice, w.bob)
	reaching(t, w.alice, conv, phone)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "own question?"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the question at the phone and at bob", func() bool {
		return slices.Contains(convBodies(t, phone, conv), "out:own question?") && slices.Contains(convBodies(t, w.bob, conv), "in:own question?")
	})
	if n := inboxCount(t, phone, `conv = ? AND state = ?`, conv, stateConvHeld); n != 1 {
		t.Fatalf("setup: %d held copies at the phone, want 1", n)
	}
	if p, err := phone.PageReview(); err != nil || len(p.Held) != 0 {
		t.Fatalf("the person's own question is held for them on the phone: %+v %v", p.Held, err)
	}
	p, err := w.bob.PageReview()
	if err != nil || len(p.Held) != 1 || p.Held[0].Body != "own question?" || p.Held[0].From != w.alice.Address {
		t.Fatalf("alice's question at bob: %+v %v", p.Held, err)
	}
	if _, err := w.bob.DeleteConversation(tctx(t), conv); err != nil {
		t.Fatal(err)
	}
	if p, err := w.bob.PageReview(); err != nil || len(p.Held) != 0 {
		t.Fatalf("a held turn of a deleted conversation is listed: %+v %v", p.Held, err)
	}
}

// A human guest's invitation names this device as its host, but no agent
// runs for it: it is the guest's own decision in the conversation, never a
// needs-you item.
func TestPageReviewLeavesOutHumanGuestInvite(t *testing.T) {
	w, carol, conv, _, _ := humanWorld(t)
	p, err := w.alice.InviteHuman(tctx(t), conv, carol.Address, nil, "join us")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the guest invitation at carol", func() bool { return stateAt(t, carol, p.PID).State == PartInvited })
	if info := stateAt(t, carol, p.PID); !info.HostHere || info.Role != protocol.RoleHuman {
		t.Fatalf("setup: %+v", info)
	}
	if r, err := carol.PageReview(); err != nil || len(r.Conv) != 0 {
		t.Fatalf("a human guest invitation is listed: %+v %v", r.Conv, err)
	}
}

// A request is listed exactly as Accept takes it: a replica copy is not
// (Accept refuses it), and one whose participation ended is not either
// (Accept would only close it: nothing can be decided on it any more).
func TestPageReviewRequestsStillDecidable(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	if err := w.bob.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); err != nil {
		t.Fatal(err)
	}
	pid := participate(t, w, conv, nil, nil)
	if _, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "rotate the key"); err != nil {
		t.Fatal(err)
	}
	var id string
	eventually(t, "the task waiting at bob", func() bool {
		w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND body = ? AND state = ?`, conv, "rotate the key", stateAwaiting).Scan(&id)
		return id != ""
	})
	if r, ok := needsYou(t, w.bob, pid, id); !ok || r.Reason != ReviewAwaiting {
		t.Fatalf("the waiting task: %+v %v", r, ok)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET replica = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := needsYou(t, w.bob, pid, id); ok {
		t.Fatal("a replica request is listed")
	}
	if err := w.bob.Accept(id); err == nil {
		t.Fatal("setup: Accept took a replica request")
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET replica = 0 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, ok := needsYou(t, w.bob, pid, id); !ok {
		t.Fatal("the task is not listed again")
	}
	if _, err := w.alice.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the participation dismissed", func() bool { return stateAt(t, w.bob, pid).State == PartDismissed })
	if r, ok := needsYou(t, w.bob, pid, id); ok {
		t.Fatalf("a request whose participation ended is listed: %+v", r)
	}
}

// In a group: an invitation for this device's agent, then a task to it
// that waits for its person, are listed with the group as their
// conversation; neither runs anything.
func TestPageReviewGroup(t *testing.T) {
	stub := installAgentStub(t)
	w, _, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	record, err := w.bob.CreateLocalAgent("Group helper", Responder{Harness: "agentstub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.PublishAgentCatalog(tctx(t)); err != nil {
		t.Fatal(err)
	}
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, w.bob.Address, record.ID, nil, nil, "group help")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the group invitation at bob", func() bool { _, ok := needsYou(t, w.bob, p.PID, ""); return ok })
	if r, _ := needsYou(t, w.bob, p.PID, ""); r.Reason != ReviewInvite || r.Conv != conv || r.From != w.alice.Address || r.Body != "group help" || r.ID != "" {
		t.Fatalf("the group invitation: %+v", r)
	}
	if _, err = w.bob.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice to see it active", func() bool { return stateAt(t, w.alice, p.PID).Claimable() })
	if _, ok := needsYou(t, w.bob, p.PID, ""); ok {
		t.Fatal("an accepted group invitation is listed")
	}
	if _, err = w.alice.AskAgent(tctx(t), p.PID, envelope.KindTask, "group task"); err != nil {
		t.Fatal(err)
	}
	var id string
	eventually(t, "the group task waiting at bob", func() bool {
		w.bob.store.db.QueryRow(`SELECT id FROM inbox WHERE conv = ? AND body = ? AND state = ?`, conv, "group task", stateAwaiting).Scan(&id)
		return id != ""
	})
	if r, ok := needsYou(t, w.bob, p.PID, id); !ok || r.Reason != ReviewAwaiting || r.Conv != conv || r.Kind != envelope.KindTask {
		t.Fatalf("the group task: %+v %v", r, ok)
	}
	if stub.runs() != 0 {
		t.Fatal("listing ran the agent")
	}
}
