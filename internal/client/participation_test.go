package client

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// dmWithHistory: alice and bob with persons, running daemons, a DM, and
// three messages from alice that bob holds; it returns their logical ids.
func dmWithHistory(t *testing.T) (*world, string, []string) {
	t.Helper()
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	for _, body := range []string{"deploy failed at step 3", "logs are in the ticket", "unrelated: lunch?"} {
		if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "bob to hold the history", func() bool { return len(convBodies(t, w.bob, conv)) == 3 })
	msgs, _ := w.alice.ConversationMessages(conv)
	var lids []string
	for _, m := range msgs {
		lids = append(lids, m.LID)
	}
	return w, conv, lids
}

func stateAt(t *testing.T, a *Agent, pid string) ParticipationInfo {
	t.Helper()
	p, err := a.Participation(pid)
	if err != nil && !errors.Is(err, ErrNoParticipation) {
		t.Fatal(err)
	}
	return p
}

// An invite names the host, exactly which earlier messages may be shared
// and which keys may ask for follow-up tasks; only the host's person accepts
// it, all or nothing; a retried accept sends the same event; the agent is
// asked but nothing runs; either member dismisses it.
func TestParticipationInviteAcceptDismiss(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	alicePub := w.alice.id.Public(w.alice.Address)
	for _, bad := range []struct {
		host, lid, key string
	}{
		{"carol/desk", lids[0], ""},
		{w.bob.Address, strings.Repeat("0", 32), ""},
		{w.bob.Address, lids[0], "00000000-00000000-00000000-00000000"},
	} {
		keys := []string{}
		if bad.key != "" {
			keys = []string{bad.key}
		}
		if _, err := w.alice.InviteAgent(tctx(t), conv, bad.host, []string{bad.lid}, keys, ""); err == nil {
			t.Fatalf("invite with host %s, grant %s, key %q accepted", bad.host, bad.lid, bad.key)
		}
	}
	p, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, lids[:2], []string{alicePub.Fingerprint()}, "please check the deploy")
	if err != nil || p.State != PartInvited || p.HostHere || p.Host.Address != w.bob.Address {
		t.Fatalf("invite: %+v %v", p, err)
	}
	pid := p.PID
	eventually(t, "bob to see the invite", func() bool { return stateAt(t, w.bob, pid).State == PartInvited })
	atBob := stateAt(t, w.bob, pid)
	if !atBob.HostHere || !slices.Equal(grantLIDs(atBob.Grant), lids[:2]) || atBob.Note != "please check the deploy" || atBob.Invite != p.Invite {
		t.Fatalf("bob's view: %+v", atBob)
	}
	if _, err := w.alice.AcceptParticipation(tctx(t), pid); err == nil {
		t.Fatal("the inviter accepted for bob's agent")
	}
	if _, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "early"); err == nil {
		t.Fatal("asked an agent that was only invited")
	}
	acc, err := w.bob.AcceptParticipation(tctx(t), pid)
	if err != nil || acc.State != PartActive || !acc.Claimable() {
		t.Fatalf("accept: %+v %v", acc, err)
	}
	events, _ := w.bob.store.participationEvents(conv, pid)
	for _, ev := range events {
		if ev.Type == protocol.EventAccept && ev.Prev != p.Invite {
			t.Fatal("the accept does not bind the invite")
		}
	}
	if again, err := w.bob.AcceptParticipation(tctx(t), pid); err != nil || again.Decision != acc.Decision {
		t.Fatalf("a retried accept: %+v %v", again, err)
	}
	if events, _ = w.bob.store.participationEvents(conv, pid); len(events) != 2 {
		t.Fatalf("a retry signed a new event: %d events", len(events))
	}
	if _, err := w.bob.DeclineParticipation(tctx(t), pid); err == nil {
		t.Fatal("the host decided twice")
	}
	eventually(t, "alice to see it active", func() bool { return stateAt(t, w.alice, pid).State == PartActive })

	// Context: the two granted earlier messages, not the third; then the
	// question to the agent. Bob has no responder: it waits for his agent,
	// and neither the legacy claim nor a legacy accept takes it.
	c, err := w.bob.ParticipationContext(pid, 0)
	if err != nil || len(c.Messages) != 2 || c.Messages[0].Body != "deploy failed at step 3" || c.Unrelated != 1 || c.Missing != 0 {
		t.Fatalf("context: %+v %v", c, err)
	}
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "what failed?")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold the question", func() bool {
		return inboxCount(t, w.bob, `id = ? AND pid = ? AND state = ?`, q.ID, pid, stateAgentWaiting) == 1
	})
	if c, _ = w.bob.ParticipationContext(pid, 0); c.Addressed != 1 || c.Messages[len(c.Messages)-1].Body != "what failed?" {
		t.Fatalf("context with the question: %+v", c)
	}
	time.Sleep(200 * time.Millisecond) // bob's worker is running
	if _, ok, _ := w.bob.store.claimJob("test"); ok || inboxCount(t, w.bob, `id = ? AND state = ?`, q.ID, stateAgentWaiting) != 1 {
		t.Fatal("participation work was claimed by the legacy worker")
	}
	if err := w.bob.Accept(q.ID); err == nil {
		t.Fatal("legacy accept ran a participation request")
	}

	// Either member dismisses; it ends it at both, for good.
	if d, err := w.alice.DismissParticipation(tctx(t), pid); err != nil || d.State != PartDismissed {
		t.Fatalf("dismiss: %+v %v", d, err)
	}
	eventually(t, "bob to see it dismissed", func() bool { return stateAt(t, w.bob, pid).State == PartDismissed })
	if _, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "late"); err == nil {
		t.Fatal("asked a dismissed agent")
	}
	if _, err := w.bob.AcceptParticipation(tctx(t), pid); stateAt(t, w.bob, pid).State != PartDismissed {
		t.Fatalf("an accept after dismissal revived it (%v)", err)
	}

	// Restart: the same events resolve to the same participation.
	w.bob.Close()
	again, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	if r := stateAt(t, again, pid); r.State != PartDismissed || r.Invite != p.Invite || !r.HostHere {
		t.Fatalf("after a restart: %+v", r)
	}
}

// eventsFor makes signed events of one participation, as alice and bob's
// devices would.
type eventMaker struct {
	t          *testing.T
	w          *world
	conv, pid  string
	alice, bob protocol.EventAuthor
	ts         int64
}

func newEventMaker(t *testing.T, w *world, conv string) *eventMaker {
	me := func(a *Agent) protocol.EventAuthor {
		p, _, _ := a.store.selfPerson()
		return protocol.EventAuthor{Person: p.info.Person, Roster: p.info.Roster, Address: a.Address, Fingerprint: p.info.Fingerprint}
	}
	return &eventMaker{t: t, w: w, conv: conv, pid: protocol.NewID(), alice: me(w.alice), bob: me(w.bob), ts: 1790000000}
}

func (m *eventMaker) make(by *Agent, typ, prev string, grant ...string) protocol.ParticipationEvent { // grant: lids of alice's messages
	m.ts++
	au := m.alice
	if by == m.w.bob {
		au = m.bob
	}
	ev := protocol.ParticipationEvent{V: 1, Conv: m.conv, PID: m.pid, Type: typ, Prev: prev, Author: au, TS: m.ts}
	if typ == protocol.EventInvite {
		ev.Host = &protocol.ParticipationHost{Person: m.bob.Person, Address: m.bob.Address, Fingerprint: m.bob.Fingerprint}
		ev.Audience = protocol.AudienceConversation
		for _, lid := range grant {
			ev.Grant = append(ev.Grant, protocol.GrantRef{LID: lid, Fingerprint: m.alice.Fingerprint})
		}
	}
	ev.Sign(by.id.Sign)
	if err := ev.Validate(); err != nil {
		m.t.Fatal(err)
	}
	return ev
}

// permutations calls f with every order of evs.
func permutations(evs []protocol.ParticipationEvent, f func([]protocol.ParticipationEvent)) {
	if len(evs) <= 1 {
		f(evs)
		return
	}
	for i := range evs {
		rest := append(append([]protocol.ParticipationEvent{}, evs[:i]...), evs[i+1:]...)
		permutations(rest, func(p []protocol.ParticipationEvent) { f(append([]protocol.ParticipationEvent{evs[i]}, p...)) })
	}
}

// The state is the same whatever order events arrive in; forks and
// competing decisions never make it active; events following one not held
// wait for it; a held dismissal keeps it from being claimable; an author
// that does not count here changes nothing.
func TestParticipationResolution(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	m, err := w.bob.dmMembers(conv)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, evs []protocol.ParticipationEvent, state string, held, heldDismiss int) {
		t.Helper()
		permutations(evs, func(order []protocol.ParticipationEvent) {
			got := resolve(conv, evs[0].PID, order, m)
			if got.State != state || got.Held != held || got.HeldDismiss != heldDismiss {
				t.Fatalf("%s: %s held %d/%d, want %s held %d/%d (order %v)", name, got.State, got.Held, got.HeldDismiss, state, held, heldDismiss, order)
			}
		})
	}
	e := newEventMaker(t, w, conv)
	inv := e.make(w.alice, protocol.EventInvite, "", lids[0])
	acc := e.make(w.bob, protocol.EventAccept, inv.Hash())
	check("invite + accept", []protocol.ParticipationEvent{inv, acc}, PartActive, 0, 0)
	check("accept before its invite", []protocol.ParticipationEvent{acc}, PartPending, 1, 0)
	dec := e.make(w.bob, protocol.EventDecline, inv.Hash())
	check("accept and decline", []protocol.ParticipationEvent{inv, acc, dec}, PartConflict, 0, 0)
	acc2 := e.make(w.bob, protocol.EventAccept, inv.Hash())
	check("two accepts", []protocol.ParticipationEvent{inv, acc, acc2}, PartConflict, 0, 0)
	check("identical accepts", []protocol.ParticipationEvent{inv, acc, acc}, PartConflict, 0, 0) // the store keeps one; resolve alone sees two copies
	byAlice := e.make(w.alice, protocol.EventAccept, inv.Hash())
	check("accept by the inviter", []protocol.ParticipationEvent{inv, byAlice}, PartInvited, 1, 0)
	inv2 := e.make(w.alice, protocol.EventInvite, "", lids[1])
	check("two invites, one id", []protocol.ParticipationEvent{inv, inv2}, PartConflict, 0, 0)
	dis := e.make(w.alice, protocol.EventDismiss, acc.Hash())
	check("dismissed by the inviter", []protocol.ParticipationEvent{inv, acc, dis}, PartDismissed, 0, 0)
	check("dismissal before the accept it follows", []protocol.ParticipationEvent{inv, dis}, PartInvited, 1, 1)
	byHost := e.make(w.bob, protocol.EventDismiss, inv.Hash())
	check("dismissed by the host before deciding", []protocol.ParticipationEvent{inv, byHost}, PartDismissed, 0, 0)
	check("dismissed despite a fork", []protocol.ParticipationEvent{inv, acc, dec, byHost}, PartDismissed, 0, 0)
	late := e.make(w.bob, protocol.EventAccept, inv.Hash())
	check("accept after dismissal", []protocol.ParticipationEvent{inv, byHost, late}, PartDismissed, 0, 0)
	if p := resolve(conv, e.pid, []protocol.ParticipationEvent{inv, acc, dis}, m); p.Claimable() {
		t.Fatal("a dismissed participation is claimable")
	}
	withHeld := resolve(conv, e.pid, []protocol.ParticipationEvent{inv, acc, e.make(w.alice, protocol.EventDismiss, strings.Repeat("f", 64))}, m)
	if withHeld.State != PartActive || withHeld.Claimable() {
		t.Fatalf("active with a held dismissal must not be claimable: %+v", withHeld)
	}

	// The store keeps each event once, whatever arrives twice.
	for range 2 {
		for _, ev := range []protocol.ParticipationEvent{inv, acc} {
			raw, _ := json.Marshal(ev)
			if err := w.bob.store.addParticipationEvent(ev, raw); err != nil {
				t.Fatal(err)
			}
		}
	}
	if evs, _ := w.bob.store.participationEvents(conv, e.pid); len(evs) != 2 {
		t.Fatalf("stored %d events", len(evs))
	}

	// An author frozen here no longer counts: nothing it signed has effect.
	me, _, _ := w.alice.store.selfPerson()
	other := me.roster
	other.Label = "someone else"
	other.Sign(w.alice.id.Sign)
	raw, _ := json.Marshal(other)
	w.bob.store.pinPerson(other, raw, w.alice.id.Public(w.alice.Address))
	frozen, _ := w.bob.dmMembers(conv)
	if p := resolve(conv, e.pid, []protocol.ParticipationEvent{inv, acc}, frozen); p.State != PartPending || p.Claimable() {
		t.Fatalf("events of a frozen person counted: %+v", p)
	}
}

// A participation event is stored only as its own sending device's, for
// its own message's conversation and participation, and well signed.
func TestParticipationEventAdmission(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	_, root := rootOf(t, w.alice, conv)
	e := newEventMaker(t, w, conv)
	send := func(ev protocol.ParticipationEvent, pid string) envelope.Envelope {
		raw, _ := json.Marshal(ev)
		env := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: string(raw), Conv: conv, LID: protocol.NewID(),
			Root: root, Sub: envelope.SubEvent, PID: pid, Origin: envelope.OriginUI})
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return env
	}
	asBob := e.make(w.bob, protocol.EventInvite, "", lids[0]) // bob's event, sent by alice
	if r := heldReason(t, w.bob, send(asBob, e.pid).ID); r != reasonInvalid {
		t.Fatalf("someone else's event: %q", r)
	}
	claimsBob := asBob
	claimsBob.Sign(w.alice.id.Sign) // names bob as its author, signed (validly) by alice, who sends it
	if r := heldReason(t, w.bob, send(claimsBob, e.pid).ID); r != reasonInvalid {
		t.Fatalf("an event naming another author: %q", r)
	}
	inv := e.make(w.alice, protocol.EventInvite, "", lids[0])
	if r := heldReason(t, w.bob, send(inv, protocol.NewID()).ID); r != reasonInvalid {
		t.Fatalf("an event for another participation: %q", r)
	}
	forged := inv
	forged.Sig = append([]byte(nil), inv.Sig...)
	forged.Sig[0] ^= 1
	if r := heldReason(t, w.bob, send(forged, e.pid).ID); r != reasonInvalid {
		t.Fatalf("a bad signature: %q", r)
	}
	if _, err := w.bob.Participation(e.pid); !errors.Is(err, ErrNoParticipation) {
		t.Fatal("a refused event was stored")
	}
	good := send(inv, e.pid)
	if r := heldReason(t, w.bob, good.ID); r != "" {
		t.Fatalf("a good event held: %q", r)
	}
	if p := stateAt(t, w.bob, e.pid); p.State != PartInvited || !p.HostHere {
		t.Fatalf("after a good invite: %+v", p)
	}
}

func grantLIDs(g []protocol.GrantRef) []string {
	var out []string
	for _, r := range g {
		out = append(out, r.LID)
	}
	return out
}
