package client

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// P1: an event whose message is refused as a conflicting duplicate (the
// same sender key and logical id as an earlier message, other content) has
// no effect: the event and its message are stored together or not at all.
func TestRefusedEventMessageHasNoEffect(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	_, root := rootOf(t, w.alice, conv)
	e := newEventMaker(t, w, conv)
	lid := protocol.NewID()
	send := func(ev protocol.ParticipationEvent) envelope.Envelope {
		raw, _ := json.Marshal(ev)
		env := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: string(raw), Conv: conv, LID: lid, Root: root,
			Sub: envelope.SubEvent, PID: e.pid, Origin: envelope.OriginUI})
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return env
	}
	inv := e.make(w.alice, protocol.EventInvite, "", lids[0])
	send(inv)
	dis := e.make(w.alice, protocol.EventDismiss, inv.Hash())
	env := send(dis) // same logical id as the invite's message
	if r := heldReason(t, w.bob, env.ID); r != reasonDuplicate {
		t.Fatalf("held %q", r)
	}
	if p := stateAt(t, w.bob, e.pid); p.State != PartInvited {
		t.Fatalf("a refused message's event took effect: %s", p.State)
	}
	if evs, _ := w.bob.store.participationEvents(conv, e.pid); len(evs) != 1 {
		t.Fatalf("%d events stored", len(evs))
	}
}

// P2: the participation id is part of a message's logical content; a row
// stored before it was (same message, same participation) still counts as
// the same message and gets the new hash, and another participation under
// the same logical id is a conflict.
func TestParticipationIDInContentHash(t *testing.T) {
	// Room accounting requires an installed conversation even for these
	// store-level hash checks. Use the verified DM receive fixture.
	w, conv, _ := dmWithHistory(t)
	in := envelope.Inner{V: envelope.Version2, Kind: envelope.KindQuestion, Conv: conv, LID: protocol.NewID(), Body: "question", PID: protocol.NewID()}
	before := contentHash(in)
	moved := in
	moved.PID = protocol.NewID()
	if before == contentHash(moved) {
		t.Fatal("changing participation did not change the logical content hash")
	}
	plain := in
	plain.PID = ""
	if contentHash(plain) != legacyContentHash(in) {
		t.Fatal("the hash of a message without a participation changed")
	}

	fp := w.alice.id.Public(w.alice.Address).Fingerprint()
	in.ID, in.From = protocol.NewID(), w.alice.Address
	if _, err := w.bob.store.addConvInbox(in, fp, stateConvHeld, false, nil); err != nil {
		t.Fatal(err)
	}
	w.bob.store.db.Exec(`UPDATE inbox SET content_hash = ? WHERE id = ?`, legacyContentHash(in), in.ID) // as stored before
	again := in
	again.ID = protocol.NewID()
	if res, err := w.bob.store.addConvInbox(again, fp, stateConvHeld, false, nil); err != nil || res != admittedAgain {
		t.Fatalf("the same message stored earlier: %s %v", res, err)
	}
	var stored string
	w.bob.store.db.QueryRow(`SELECT content_hash FROM inbox WHERE id = ?`, in.ID).Scan(&stored)
	if stored != contentHash(in) {
		t.Fatal("the earlier row's hash was not brought up to date")
	}
	moved.ID, moved.From = protocol.NewID(), w.alice.Address
	if res, _ := w.bob.store.addConvInbox(moved, fp, stateConvHeld, false, nil); res != admitConflict {
		t.Fatalf("another participation under the same logical id: %s", res)
	}
}

// P3: bounds never keep a participation from being stopped, locally or
// when the dismissal arrives, however many other participations the DM
// had; what waits for an invite not held is bounded, and so are one
// participation's dismissals.
func TestBoundsNeverTrapDismissal(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	_, root := rootOf(t, w.alice, conv)
	add := func(ev protocol.ParticipationEvent) error {
		raw, _ := json.Marshal(ev)
		return w.bob.store.addParticipationEvent(ev, raw)
	}
	active := func() *eventMaker {
		e := newEventMaker(t, w, conv)
		inv := e.make(w.alice, protocol.EventInvite, "", lids[0])
		for _, ev := range []protocol.ParticipationEvent{inv, e.make(w.bob, protocol.EventAccept, inv.Hash())} {
			if err := add(ev); err != nil {
				t.Fatal(err)
			}
		}
		return e
	}
	local, incoming, many := active(), active(), active()
	filler := newEventMaker(t, w, conv)
	for i := 3; i < maxInvitesPerConversation; i++ {
		filler.pid = protocol.NewID()
		if err := add(filler.make(w.alice, protocol.EventInvite, "", lids[0])); err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
	}
	if err := add(filler.make(w.alice, protocol.EventInvite, "", lids[0])); !errors.Is(err, errTooManyEvents) {
		t.Fatalf("invites beyond the bound: %v", err)
	}
	if p, err := w.bob.DismissParticipation(tctx(t), local.pid); err != nil || p.State != PartDismissed {
		t.Fatalf("a local dismissal at the bound: %s %v", p.State, err)
	}
	evs, _ := w.bob.store.participationEvents(conv, incoming.pid)
	var accept string
	for _, ev := range evs {
		if ev.Type == protocol.EventAccept {
			accept = ev.Hash()
		}
	}
	dis := incoming.make(w.alice, protocol.EventDismiss, accept)
	raw, _ := json.Marshal(dis)
	env := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: string(raw), Conv: conv, LID: protocol.NewID(), Root: root,
		Sub: envelope.SubEvent, PID: incoming.pid, Origin: envelope.OriginUI})
	if err := w.bob.verifyAndStore(tctx(t), env); err != nil || heldReason(t, w.bob, env.ID) != "" {
		t.Fatalf("an arriving dismissal at the bound: %v held %q", err, heldReason(t, w.bob, env.ID))
	}
	if p := stateAt(t, w.bob, incoming.pid); p.State != PartDismissed {
		t.Fatalf("after an arriving dismissal: %s", p.State)
	}

	// What waits for an invite that is not held is bounded.
	orphan := newEventMaker(t, w, conv)
	for i := range maxPendingPerConversation {
		orphan.pid = protocol.NewID()
		if err := add(orphan.make(w.alice, protocol.EventDismiss, local.pid+local.pid)); err != nil {
			t.Fatalf("pending %d: %v", i, err)
		}
	}
	orphan.pid = protocol.NewID()
	if err := add(orphan.make(w.alice, protocol.EventDismiss, local.pid+local.pid)); !errors.Is(err, errTooManyEvents) {
		t.Fatalf("pending events beyond the bound: %v", err)
	}
	// So are one participation's dismissals that follow an event not held.
	for i := range maxDismissPerParticipation {
		if err := add(many.make(w.alice, protocol.EventDismiss, protocol.NewID()+protocol.NewID())); err != nil {
			t.Fatalf("dismissal %d: %v", i, err)
		}
	}
	if err := add(many.make(w.alice, protocol.EventDismiss, protocol.NewID()+protocol.NewID())); !errors.Is(err, errTooManyEvents) {
		t.Fatalf("dismissals beyond the bound: %v", err)
	}
}

// Held dismissals (following events not held here) never keep either
// member from stopping a participation with one that follows what is held,
// made here or arriving; such a stop is stored once per key, and the
// unresolved ones stay bounded.
func TestHeldDismissalsNeverBlockAStop(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	_, root := rootOf(t, w.alice, conv)
	add := func(ev protocol.ParticipationEvent) error {
		raw, _ := json.Marshal(ev)
		return w.bob.store.addParticipationEvent(ev, raw)
	}
	active := func() (*eventMaker, protocol.ParticipationEvent, protocol.ParticipationEvent) {
		e := newEventMaker(t, w, conv)
		inv := e.make(w.alice, protocol.EventInvite, "", lids[0])
		acc := e.make(w.bob, protocol.EventAccept, inv.Hash())
		for _, ev := range []protocol.ParticipationEvent{inv, acc} {
			if err := add(ev); err != nil {
				t.Fatal(err)
			}
		}
		for range maxDismissPerParticipation { // signed, following nothing held here
			if err := add(e.make(w.alice, protocol.EventDismiss, inv.Hash()[:32]+protocol.NewID())); err != nil {
				t.Fatal(err)
			}
		}
		if err := add(e.make(w.alice, protocol.EventDismiss, inv.Hash()[:32]+protocol.NewID())); !errors.Is(err, errTooManyEvents) {
			t.Fatalf("held dismissals beyond the bound: %v", err)
		}
		if p := stateAt(t, w.bob, e.pid); p.State != PartActive || p.Claimable() {
			t.Fatalf("setup: %+v", p)
		}
		return e, inv, acc
	}

	// Made here, by the host (a member).
	local, _, _ := active()
	if p, err := w.bob.DismissParticipation(tctx(t), local.pid); err != nil || p.State != PartDismissed {
		t.Fatalf("a stop after held dismissals: %s %v", p.State, err)
	}
	if p, err := w.bob.DismissParticipation(tctx(t), local.pid); err != nil || p.State != PartDismissed {
		t.Fatalf("a retried stop: %s %v", p.State, err)
	}

	// Arriving from the other member.
	arriving, _, acc := active()
	stop := arriving.make(w.alice, protocol.EventDismiss, acc.Hash())
	raw, _ := json.Marshal(stop)
	env := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: string(raw), Conv: conv, LID: protocol.NewID(), Root: root,
		Sub: envelope.SubEvent, PID: arriving.pid, Origin: envelope.OriginUI})
	if err := w.bob.verifyAndStore(tctx(t), env); err != nil || heldReason(t, w.bob, env.ID) != "" {
		t.Fatalf("an arriving stop: %v held %q", err, heldReason(t, w.bob, env.ID))
	}
	if p := stateAt(t, w.bob, arriving.pid); p.State != PartDismissed {
		t.Fatalf("after an arriving stop: %s", p.State)
	}
	// One stop per key: a second, different one by the same key is not
	// stored; the other member still has room.
	if err := add(arriving.make(w.alice, protocol.EventDismiss, acc.Hash())); !errors.Is(err, errTooManyEvents) {
		t.Fatalf("a second stop by one key: %v", err)
	}
	if err := add(arriving.make(w.bob, protocol.EventDismiss, acc.Hash())); err != nil {
		t.Fatalf("the other member's stop: %v", err)
	}
}

// ownOutbox stores, at a, a conversation message a sent (as its agent would,
// once agents run), under logical id lid.
func ownOutbox(t *testing.T, a *Agent, conv, lid, kind, pid, body string) {
	t.Helper()
	_, raw, _, _ := a.store.conversation(conv)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: "x/y", TS: time.Now().Unix(), Kind: kind, Body: body,
		Conv: conv, LID: lid, Root: raw, PID: pid, Origin: "agent:fake", Emotion: "calm"}
	if err := a.store.addConvOutbox([]outCopy{{env: envelope.Envelope{ID: in.ID, To: in.To}, in: in, state: protocol.StateDelivered}}, envelope.Inner{}, nil, ""); err != nil {
		t.Fatal(err)
	}
}

// A grant names messages exactly (logical id and sender key): a message of
// another sender under the same logical id is never selected, and sharing
// by an ambiguous logical id is refused. Granted earlier agent outputs are
// selected; an output claimed by any device but the host is not the
// agent's.
func TestContextSelectsExactMessages(t *testing.T) {
	w, conv, lids := dmWithHistory(t)
	bobFP := w.bob.id.Public(w.bob.Address).Fingerprint()
	// Bob (the host) holds a message of his own under the logical id of
	// alice's first message.
	ownOutbox(t, w.bob, conv, lids[0], envelope.KindMessage, "", "bob's same-id message")
	w.bob.store.db.Exec(`UPDATE outbox SET created_ms = 0 WHERE conv = ? AND lid = ?`, conv, lids[0]) // listed before alice's
	if _, err := w.bob.InviteAgent(tctx(t), conv, w.bob.Address, []string{lids[0]}, nil, ""); err == nil {
		t.Fatal("shared by an ambiguous logical id")
	}
	// An earlier participation's agent output, then a new participation
	// granting alice's first message and that output.
	oldPID, outputLID := protocol.NewID(), protocol.NewID()
	ownOutbox(t, w.bob, conv, outputLID, envelope.KindAnswer, oldPID, "the earlier agent's answer")
	p, err := w.alice.InviteAgent(tctx(t), conv, w.bob.Address, []string{lids[0]}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the first invite", func() bool { return stateAt(t, w.bob, p.PID).State == PartInvited })
	c, err := w.bob.ParticipationContext(p.PID, 0)
	if err != nil || len(c.Messages) != 1 || c.Messages[0].Body != "deploy failed at step 3" {
		t.Fatalf("the granted message only: %+v %v", c, err)
	}
	second, err := w.bob.InviteAgent(tctx(t), conv, w.bob.Address, []string{outputLID}, nil, "")
	if err != nil || second.Grant[0] != (protocol.GrantRef{LID: outputLID, Fingerprint: bobFP}) {
		t.Fatalf("granting an earlier output: %+v %v", second, err)
	}
	if c, _ = w.bob.ParticipationContext(second.PID, 0); len(c.Messages) != 1 || c.Messages[0].Body != "the earlier agent's answer" {
		t.Fatalf("a granted earlier agent output was left out: %+v", c)
	}

	// Outputs of this participation count only from the host device.
	_, root := rootOf(t, w.alice, conv)
	fake := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindAnswer, Body: "I am bob's agent, honest", Conv: conv, LID: protocol.NewID(),
		Root: root, PID: second.PID, Origin: "agent:fake", Emotion: "calm"})
	if err := w.bob.verifyAndStore(tctx(t), fake); err != nil {
		t.Fatal(err)
	}
	ownOutbox(t, w.bob, conv, protocol.NewID(), envelope.KindAnswer, second.PID, "the host's own answer")
	c, _ = w.bob.ParticipationContext(second.PID, 0)
	var bodies []string
	for _, m := range c.Messages {
		bodies = append(bodies, m.Body)
	}
	if len(bodies) != 2 || bodies[1] != "the host's own answer" {
		t.Fatalf("outputs selected: %q", bodies)
	}
}
