package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// relabelled publishes, from a's own key, a person record like its current
// one with another label, as a real (re)publication would.
func relabelled(t *testing.T, a *Agent, label string) {
	t.Helper()
	me, _, err := a.store.selfPerson()
	if err != nil {
		t.Fatal(err)
	}
	other := me.roster
	other.Label = label
	other.Sign(a.id.Sign)
	raw, _ := json.Marshal(other)
	if err := a.hub.doBytes(tctx(t), "PUT", "/v1/person", raw, nil); err != nil {
		t.Fatal(err)
	}
}

// R1 as root reproduced it: bob pinned alice's person; alice publishes a
// different, validly signed record; the member list bob receives carries it
// and bob freezes alice's person, keeping the one he pinned.
func TestPublishedPersonConflictFromMemberList(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	pub := w.alice.id.Public(w.alice.Address)
	before, err := w.bob.personOfKey(tctx(t), w.alice.Address, pub)
	if err != nil {
		t.Fatal(err)
	}
	relabelled(t, w.alice, "Different signed roster")
	var members protocol.Members
	if err := w.bob.hub.do(tctx(t), "GET", "/v1/agents", nil, &members); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(members)
	w.bob.onMembers(data)
	w.bob.convSync(tctx(t))
	got, err := w.bob.personOfKey(tctx(t), w.alice.Address, pub)
	if !errors.Is(err, errPersonConflict) {
		t.Fatalf("published conflicting signed roster ignored: state=%s err=%v", got.info.State, err)
	}
	if got.info.Person != before.info.Person || got.info.Roster != before.info.Roster || got.info.Label != before.info.Label {
		t.Fatalf("the pinned identity changed: %+v, was %+v", got.info, before.info)
	}
}

// R1 through the running daemons: the Hub's push after alice republishes
// freezes her person at bob; bob can no longer send in their DM, and a new
// message from alice is held as a conflict, not admitted.
func TestPublishedPersonConflictFreezesDM(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to pin alice by admitting her message", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	relabelled(t, w.alice, "Alice (new label)")
	eventually(t, "bob to freeze alice's person on the push", func() bool {
		p, _, _ := w.bob.store.personByAddress(w.alice.Address)
		return p.info.State == personConflict
	})
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "still there?"}); !errors.Is(err, errPersonConflict) {
		t.Fatalf("bob sent into a frozen DM: %v", err)
	}
	s, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "after the change"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "alice's new message held as a conflict", func() bool { return heldReason(t, w.bob, s.ID) == reasonConflict })
	if n := len(convBodies(t, w.bob, conv)); n != 1 {
		t.Fatalf("bob's DM holds %d messages after the conflict", n)
	}
}

// Evidence that does not verify against the pinned key, a member without a
// record, and members nobody pinned change nothing: no conflict is made up
// and nobody is pinned for being listed.
func TestMemberListNeverFabricatesOrPins(t *testing.T) {
	w := newWorld(t, "")
	carol := mustJoin(t, t.TempDir(), w.aliceInvites("carol"), "desk")
	persons(t, w.alice, w.bob, carol)
	pub := w.alice.id.Public(w.alice.Address)
	if _, err := w.bob.personOfKey(tctx(t), w.alice.Address, pub); err != nil {
		t.Fatal(err)
	}
	me, _, _ := w.alice.store.selfPerson()
	stranger, _ := identity.Generate()
	forged := me.roster
	forged.Label = "Mallory"
	forged.Sign(stranger.Sign) // not alice's key
	forgedRaw, _ := json.Marshal(forged)
	elsewhere := me.roster
	elsewhere.Label = "Elsewhere"
	elsewhere.Devices = []protocol.RosterDevice{{Address: w.alice.Address, Fingerprint: stranger.Public(w.alice.Address).Fingerprint()}}
	elsewhere.Sign(w.alice.id.Sign) // alice's key, but naming another device key
	elsewhereRaw, _ := json.Marshal(elsewhere)
	var members protocol.Members
	if err := w.bob.hub.do(tctx(t), "GET", "/v1/agents", nil, &members); err != nil {
		t.Fatal(err)
	}
	for _, blob := range []json.RawMessage{forgedRaw, elsewhereRaw, nil, json.RawMessage(`{"person":"x"}`)} {
		for i := range members.Members {
			if members.Members[i].Address == w.alice.Address {
				members.Members[i].Person = blob
			}
		}
		data, _ := json.Marshal(members)
		w.bob.onMembers(data)
		w.bob.convSync(tctx(t))
		p, _, _ := w.bob.store.personByAddress(w.alice.Address)
		if p.info.State != personPinned {
			t.Fatalf("record %.40s made a conflict", blob)
		}
	}
	if _, ok, _ := w.bob.store.personByAddress(carol.Address); ok {
		t.Fatal("a listed member was pinned for being listed")
	}
}

// holdUnknownRoots holds n correctly signed messages from carol whose
// conversation bob cannot prove (not from its creator), all older than
// anything else held.
func holdUnknownRoots(t *testing.T, w *world, carol *Agent, conv string, raw json.RawMessage, n int) {
	t.Helper()
	for i := range n {
		env := craft(t, carol, w.bob, envelope.Inner{ID: fmt.Sprintf("%032x", i+1), Kind: envelope.KindMessage, Body: "unknown root sender",
			Conv: conv, LID: protocol.NewID(), Root: raw})
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if got := heldReason(t, w.bob, env.ID); got != reasonProof {
			t.Fatalf("setup: %s", got)
		}
		w.bob.store.db.Exec(`UPDATE quarantine SET received_at = 0 WHERE id = ?`, env.ID)
	}
}

func setupProofQueue(t *testing.T, n int) (*world, *Agent, envelope.Envelope) {
	t.Helper()
	w := newWorld(t, "")
	stop := runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	stop()
	carol := mustJoin(t, t.TempDir(), w.aliceInvites("carol"), "desk")
	_, raw := rootOf(t, w.alice, conv)
	for _, a := range []*Agent{w.alice, carol} {
		if _, err := w.bob.sendKey(tctx(t), a.Address); err != nil {
			t.Fatal(err)
		}
	}
	holdUnknownRoots(t, w, carol, conv, raw, n)
	valid := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: "proof now available", Conv: conv, LID: protocol.NewID(), Root: raw})
	if err := w.bob.store.holdAs(valid, reasonProof); err != nil {
		t.Fatal(err)
	}
	return w, carol, valid
}

// drain does what a recovery event does in the daemon: the look starts
// from the beginning and continues page by page until it reaches the end
// (the daemon kicks its own next sync; here the loop stands in for that).
func drain(t *testing.T, a *Agent) (pages int) {
	t.Helper()
	a.convWork.due(convRetry)
	for a.convWork.bits.Load()&(convRetry|convRetryMore) != 0 {
		a.convSync(tctx(t))
		if pages++; pages > 100 {
			t.Fatal("the look at held messages does not end")
		}
	}
	return pages
}

// R2 as root reproduced it: 50 messages that stay unprovable come first;
// the valid one after them is reached (here within three looks).
func TestProofQueueReachesLaterMessages(t *testing.T) {
	w, _, valid := setupProofQueue(t, 50)
	for range 3 {
		w.bob.retryProof(tctx(t))
	}
	if inboxCount(t, w.bob, "id = ?", valid.ID) != 1 {
		t.Fatal("valid message after 50 unresolved messages never re-examined across three recovery events")
	}
	if n := inboxCount(t, w.bob, `sender = ?`, "carol/desk"); n != 0 {
		t.Fatal("an unprovable message was admitted")
	}
	var held int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE reason = ?`, reasonProof).Scan(&held)
	if held != 50 {
		t.Fatalf("%d unprovable messages still held, want 50", held)
	}
}

// One recovery event reaches every held message, a page per sync, even
// past many unprovable ones; after a restart part-way, the next event
// starts over and still gets there.
func TestProofQueueOneEventAndRestart(t *testing.T) {
	w, _, valid := setupProofQueue(t, 130)
	if pages := drain(t, w.bob); pages != 3 || inboxCount(t, w.bob, "id = ?", valid.ID) != 1 {
		t.Fatalf("one event: %d pages, valid admitted %v", pages, inboxCount(t, w.bob, "id = ?", valid.ID) == 1)
	}

	w2, _, valid2 := setupProofQueue(t, 130)
	w2.bob.convWork.due(convRetry)
	w2.bob.convSync(tctx(t)) // the first page only, then the daemon stops
	w2.bob.Close()
	again, err := Open(w2.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { again.Close() })
	again.Logf = t.Logf
	drain(t, again)
	if inboxCount(t, again, "id = ?", valid2.ID) != 1 {
		t.Fatal("after a restart the valid message was not reached")
	}
	var held int
	again.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE reason = ?`, reasonProof).Scan(&held)
	if held != 130 {
		t.Fatalf("%d unprovable messages still held, want 130", held)
	}
}
