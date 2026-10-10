package client

import (
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// listMembers feeds a a members push naming addresses, with suspended
// devices marked so, as the relay lists them.
func listMembers(t *testing.T, a *Agent, suspended map[string]bool, addresses ...string) {
	t.Helper()
	var m protocol.Members
	for _, address := range addresses {
		m.Members = append(m.Members, protocol.Member{Address: address, Presence: protocol.PresenceConnected, Joined: 1, Suspended: suspended[address]})
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	a.onMembers(raw)
}

// A device the relay suspended until it updates AgentNet holds nobody up:
// its waiting copy is not re-checked on every push (no profile reads),
// the message's delivery reads as everyone else's, its copy says why, and
// a send that waits for delivery does not wait for it. Once the relay
// lists it current, what it can read is released.
func TestSuspendedPeerHoldsNobody(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "to be deleted"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob has it", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapControl))
	if _, err := w.alice.Retract(tctx(t), ControlRef{Conv: conv, ID: sent.LID, Fingerprint: w.alice.Self().Fingerprint()}, ""); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := w.alice.store.db.QueryRow(`SELECT id FROM outbox WHERE sub=? AND recipient=?`, envelope.SubRetraction, w.bob.Address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	listMembers(t, w.alice, map[string]bool{w.bob.Address: true}, w.alice.Address, w.bob.Address)
	if !w.alice.SuspendedDevices()[w.bob.Address] {
		t.Fatal("suspension not kept")
	}
	signCapsAfter(t, w.bob, ownCaps) // bob reads it now, but the relay still serves him nothing
	counter := &profileCounter{base: w.alice.hub.http.Transport}
	w.alice.hub.http.Transport = counter
	feats, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		w.alice.releaseConv(tctx(t), feats)
	}
	if n := counter.n.Load(); n != 0 {
		t.Fatalf("%d profile reads for a suspended device's waiting copy", n)
	}
	if st := outboxState(t, w.alice, id); st != stateConvWaiting {
		t.Fatalf("suspended device's copy: %s", st)
	}

	// Its copy says why and decides nothing for the message.
	copies, err := w.alice.SentCopies(sent.LID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range copies {
		if c.To == w.bob.Address && !c.Suspended {
			t.Fatalf("bob's copy not marked suspended: %+v", c)
		}
	}
	if got := deliveryOf([]ConvCopy{{To: "carol/desk", Person: "carol", State: protocol.StateDelivered}, {To: w.bob.Address, Person: "bob", State: stateConvWaiting, Suspended: true}}); got != protocol.StateDelivered {
		t.Fatalf("headline with one suspended device: %q", got)
	}
	if got := deliveryOf([]ConvCopy{{To: w.bob.Address, Person: "bob", State: protocol.StateCustody, Suspended: true}, {To: "alice/phone", Own: true, State: protocol.StateDelivered}}); got != protocol.StateCustody {
		t.Fatalf("headline when only a suspended device is anyone else's: %q", got)
	}

	// A send that waits for delivery does not wait for it.
	start := time.Now()
	res, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "no waiting", Wait: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 3*time.Second || res.Detail != SuspendedText(w.bob.Address) {
		t.Fatalf("waited %s for a suspended device: %+v", time.Since(start), res)
	}
	// Nor does a command that waits for an answer from it (a stand-in for
	// the daemon's change socket: nothing else wakes it).
	ln, err := net.Listen("unix", changesSockPath(w.alice.home))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "are you there"})
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	r, err := w.alice.AwaitReply(tctx(t), q.ID, 5*time.Second, nil)
	if err != nil || r.Suspended != w.bob.Address || time.Since(start) > 3*time.Second {
		t.Fatalf("waited %s for an answer from a suspended device: %+v %v", time.Since(start), r, err)
	}

	// Listed current again: released as deliver would hand it over.
	listMembers(t, w.alice, nil, w.alice.Address, w.bob.Address)
	w.alice.releaseConv(tctx(t), feats)
	if st := outboxState(t, w.alice, id); st != stateQueued {
		t.Fatalf("not released once current: %s", st)
	}
}

// Own-device direct history is not sealed for a device that cannot read
// it now: its job keeps its cursor, and the next member list after the
// device can read it produces the history (CG-9: hundreds of copies piled
// up waiting for one outdated device).
func TestDeviceHistoryWaitsForItsReader(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	if _, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "direct question"}); err != nil {
		t.Fatal(err)
	}
	stopPhone := runAgent(t, phone)
	waitNamedAgentCaps(t, phone)
	signCapsAfter(t, phone, without(ownCaps, protocol.CapOwnSyncV3)) // an older program
	stopPhone()
	produced := func() (copies, jobs int) {
		t.Helper()
		if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=?`, phone.Address, envelope.SubDeviceHistory).Scan(&copies); err != nil {
			t.Fatal(err)
		}
		if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM device_history_jobs WHERE device=?`, phone.Address).Scan(&jobs); err != nil {
			t.Fatal(err)
		}
		return
	}
	for range 4 {
		w.alice.convWork.due(convHistory)
		w.alice.convSync(tctx(t))
	}
	if copies, jobs := produced(); copies != 0 || jobs != 0 {
		t.Fatalf("sealed for a device that cannot read it: %d copies, %d jobs", copies, jobs)
	}
	stopPhone = runAgent(t, phone) // it updates
	waitNamedAgentCaps(t, phone)
	signCapsAfter(t, phone, ownCaps)
	// Every session the relay still lists reads it: the old one lingers in
	// its grace period, and the new one may connect only after the caps
	// above were signed and publish its own a moment later.
	eventually(t, "alice reads the phone as reading own3", func() bool {
		return w.alice.requireParticipationCaps(tctx(t), phone.Self(), protocol.CapOwnSyncV3) == nil
	})
	stopPhone()
	listMembers(t, w.alice, nil, w.alice.Address, w.bob.Address, phone.Address)
	w.alice.convSync(tctx(t)) // the member list alone wakes the waiting history
	if copies, _ := produced(); copies == 0 {
		t.Fatal("no direct history once the device reads it")
	}
}
