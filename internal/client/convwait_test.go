package client

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A message kept as waiting is not released to a person who, by the time
// the device can read conversations again, has published a different
// record: the profile read freezes the person and the message stays
// waiting, with its content and reason, and is never delivered.
func TestWaitingNotReleasedToFrozenPerson(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	label, name, _ := protocol.SplitAddress(w.bob.Address)
	var prof protocol.Profile
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &prof); err != nil || len(prof.Sessions) != 1 {
		t.Fatalf("profile: %+v %v", prof, err)
	}
	publish := func(ts int64, caps ...string) {
		rec := protocol.CapsRecord{Address: w.bob.Address, Session: prof.Sessions[0], Caps: caps, TS: ts}
		rec.Sign(w.bob.id.Sign)
		if err := w.bob.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish(time.Now().Unix() + 100)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "waiting confidential DM"})
	if err != nil || sent.State != stateConvWaiting {
		t.Fatalf("setup: %+v %v", sent, err)
	}
	var body, reason string
	w.alice.store.db.QueryRow(`SELECT body, error FROM outbox WHERE id = ?`, sent.ID).Scan(&body, &reason)

	relabelled(t, w.bob, "Conflicting Bob")
	publish(time.Now().Unix()+200, protocol.CapEnv2)
	feats, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // a later event changes nothing either
		w.alice.releaseConv(tctx(t), feats)
		if err := w.alice.FlushOutbox(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := w.alice.store.personByAddress(w.bob.Address)
	if err != nil || p.info.State != personConflict {
		t.Fatalf("conflict missing: %+v %v", p.info, err)
	}
	var state, body2, reason2 string
	w.alice.store.db.QueryRow(`SELECT state, body, error FROM outbox WHERE id = ?`, sent.ID).Scan(&state, &body2, &reason2)
	if state != stateConvWaiting || body2 != body || reason2 != reason {
		t.Fatalf("frozen peer DM released or changed: state=%s body=%q reason=%q", state, body2, reason2)
	}
	time.Sleep(200 * time.Millisecond)
	if n := inboxCount(t, w.bob, `body = ?`, "waiting confidential DM"); n != 0 {
		t.Fatal("delivered to a frozen person")
	}
}

// A conversation message already queued (the Hub was out of reach) is not
// sent once its person is frozen; other messages are unaffected.
func TestQueuedNotSentToFrozenPerson(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	_, raw := rootOf(t, w.alice, conv)
	r, _ := w.bob.id.Public(w.bob.Address).Recipient()
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: "queued before the conflict", Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI}
	env, err := envelope.Seal(in, w.alice.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.addConvOutbox(env, in, stateQueued, "", nil, ""); err != nil {
		t.Fatal(err)
	}
	me, _, _ := w.bob.store.selfPerson()
	other := me.roster
	other.Label = "Someone else"
	other.Sign(w.bob.id.Sign)
	otherRaw, _ := json.Marshal(other)
	if err := w.alice.store.pinPerson(other, otherRaw, w.bob.id.Public(w.bob.Address)); !errors.Is(err, errPersonConflict) {
		t.Fatalf("setup conflict: %v", err)
	}
	plain, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "a plain message still goes"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the plain message", func() bool { return inboxCount(t, w.bob, `id = ?`, plain.ID) == 1 })
	var state string
	w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id = ?`, env.ID).Scan(&state)
	if state != stateQueued || inboxCount(t, w.bob, `id = ?`, env.ID) != 0 {
		t.Fatalf("a queued DM message went to a frozen person: %s", state)
	}
}

// Starting a DM reads the person's profile afresh; if that shows a
// different record, the person is frozen and no DM is started.
func TestCreateDMRefusesFreshConflict(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	newDM(t, w.alice, w.bob) // pins bob's person at alice
	relabelled(t, w.bob, "Conflicting Bob")
	if _, err := w.alice.CreateDM(tctx(t), w.bob.Address); !errors.Is(err, errPersonConflict) {
		t.Fatalf("a DM was started with a person whose record changed: %v", err)
	}
	if convs, _ := w.alice.Conversations(); len(convs) != 1 {
		t.Fatalf("%d conversations", len(convs))
	}
}
