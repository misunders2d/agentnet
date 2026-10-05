package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// relabel publishes, from a's own device, the next step of its person's
// roster with another label (an honest change) and pins it at a.
func relabel(t *testing.T, a *Agent, label string) {
	t.Helper()
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	r := protocol.PersonRoster{Person: me.info.Person, Label: label, Seq: me.info.Seq + 1, Prev: me.info.Roster, Devices: me.roster.Devices, By: a.Self().Fingerprint()}
	r.Sign(a.id.Sign)
	raw, _ := json.Marshal(r)
	if err := a.hub.doBytes(tctx(t), "PUT", "/v1/person", raw, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.pinChain(r.Person, [][]byte{raw}, a.Self(), false); err != nil {
		t.Fatal(err)
	}
}

// A person's next roster step, named by the member list, is fetched and
// pinned (verified against the step before it); a validly signed other
// step at a pinned seq freezes the person, keeping what was pinned.
func TestRosterStepsFromMemberList(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	pub := w.alice.id.Public(w.alice.Address)
	before, err := w.bob.personOfKey(tctx(t), w.alice.Address, pub)
	if err != nil {
		t.Fatal(err)
	}
	relabel(t, w.alice, "Alice (new label)")
	var members protocol.Members
	if err := w.bob.hub.do(tctx(t), "GET", "/v1/agents", nil, &members); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(members)
	w.bob.onMembers(data)
	w.bob.convSync(tctx(t))
	got, err := w.bob.personOfKey(tctx(t), w.alice.Address, pub)
	if err != nil || got.info.Person != before.info.Person || got.info.Seq != 1 || got.info.Label != "Alice (new label)" || got.info.State != personPinned {
		t.Fatalf("after the step: %+v %v", got.info, err)
	}
	if !w.bob.store.inChain(before.info.Person, before.info.Roster) {
		t.Fatal("the earlier step left the chain")
	}
	freeze(t, w.bob, w.alice) // alice's key signs another step 1
	got, err = w.bob.personOfKey(tctx(t), w.alice.Address, pub)
	if !errors.Is(err, errPersonConflict) || got.info.Label != "Alice (new label)" || got.info.Seq != 1 {
		t.Fatalf("a verified fork: state=%s %+v %v", got.info.State, got.info, err)
	}
}

// A frozen person freezes the DM: bob can no longer send in it, and a new
// message from alice is held as a conflict, not admitted.
func TestFrozenPersonFreezesDM(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to pin alice by admitting her message", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	freeze(t, w.bob, w.alice)
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

// Member references the Hub cannot back (another hash at the pinned seq, a
// seq it does not hold), members without one, and members nobody pinned
// change nothing: no conflict is made up and nobody is pinned for being
// listed.
func TestMemberListNeverFabricatesOrPins(t *testing.T) {
	w := newWorld(t, "")
	carol := mustJoin(t, t.TempDir(), w.aliceInvites("carol"), "desk")
	persons(t, w.alice, w.bob, carol)
	pub := w.alice.id.Public(w.alice.Address)
	pinned, err := w.bob.personOfKey(tctx(t), w.alice.Address, pub)
	if err != nil {
		t.Fatal(err)
	}
	var members protocol.Members
	if err := w.bob.hub.do(tctx(t), "GET", "/v1/agents", nil, &members); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []*protocol.PersonRef{
		{ID: pinned.info.Person, Seq: 0, Hash: strings.Repeat("a", 64)},
		{ID: pinned.info.Person, Seq: 7, Hash: strings.Repeat("b", 64)},
		nil,
	} {
		for i := range members.Members {
			if members.Members[i].Address == w.alice.Address {
				members.Members[i].Person = ref
			}
		}
		data, _ := json.Marshal(members)
		w.bob.onMembers(data)
		w.bob.convSync(tctx(t))
		p, _, _ := w.bob.store.personByAddress(w.alice.Address)
		if p.info.State != personPinned || p.info.Roster != pinned.info.Roster {
			t.Fatalf("reference %+v changed the pinned person: %+v", ref, p.info)
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
	t.Parallel()
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
