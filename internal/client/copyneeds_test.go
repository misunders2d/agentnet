package client

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// profileCounter counts the profile reads one agent makes.
type profileCounter struct {
	base http.RoundTripper
	n    atomic.Int64
}

func (c *profileCounter) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/profile") {
		c.n.Add(1)
	}
	return c.base.RoundTrip(r)
}

func releasePasses(t *testing.T, a *Agent, n int) {
	t.Helper()
	feats, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for range n {
		a.releaseConv(tctx(t), feats)
		_ = a.FlushOutbox(tctx(t))
	}
}

// A deletion parked for a reader without ctl3 (required_cap ctl3) is
// released and delivered once the reader advertises ctl3: release decides
// by the same requirements deliver does (CG-6: it asked for ctl3 as an
// unknown requirement and kept the deletion waiting forever).
func TestWaitingDeletionReleasedAfterReaderUpdates(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "to be deleted"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob has it", func() bool { return len(convBodies(t, w.bob, conv)) == 1 })
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapControl)) // an older reader
	if _, err := w.alice.Retract(tctx(t), ControlRef{Conv: conv, ID: sent.LID, Fingerprint: w.alice.Self().Fingerprint()}, ""); err != nil {
		t.Fatal(err)
	}
	var id, required string
	if err := w.alice.store.db.QueryRow(`SELECT id,coalesce(required_cap,'') FROM outbox WHERE sub=? AND recipient=?`, envelope.SubRetraction, w.bob.Address).Scan(&id, &required); err != nil {
		t.Fatal(err)
	}
	if st := outboxState(t, w.alice, id); st != stateConvWaiting || required != protocol.CapControl {
		t.Fatalf("precondition: deletion %s required %q", st, required)
	}
	releasePasses(t, w.alice, 2)
	if st := outboxState(t, w.alice, id); st != stateConvWaiting {
		t.Fatalf("released to a reader without ctl3: %s", st)
	}
	signCapsAfter(t, w.bob, ownCaps) // bob updates
	releasePasses(t, w.alice, 1)
	eventually(t, "the deletion is handed over after bob updated", func() bool {
		st := outboxState(t, w.alice, id)
		return st == protocol.StateCustody || st == protocol.StateDelivered
	})
	eventually(t, "bob sees it deleted", func() bool {
		msgs, err := w.bob.ConversationMessages(conv)
		return err == nil && len(msgs) == 1 && msgs[0].Deleted
	})
}

// The same for a deletion in a device thread, which has no person to gate
// it: only the reader's capability decides (CG-6).
func TestWaitingDeviceThreadDeletionReleased(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	hello, err := w.alice.Send(tctx(t), w.bob.Address, "thread text to delete", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds it", func() bool { return inboxCount(t, w.bob, `id = ?`, hello.ID) == 1 })
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapControl))
	ref, err := w.alice.RefOf("", hello.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.Retract(tctx(t), ref, ""); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := w.alice.store.db.QueryRow(`SELECT id FROM outbox WHERE sub=? AND recipient=?`, envelope.SubRetraction, w.bob.Address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if st := outboxState(t, w.alice, id); st != stateConvWaiting {
		t.Fatalf("precondition: %s", st)
	}
	signCapsAfter(t, w.bob, ownCaps)
	releasePasses(t, w.alice, 1)
	eventually(t, "bob sees the deletion", func() bool {
		var n int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND body=''`, hello.ID).Scan(&n)
		return n == 1
	})
}

// A group deletion to a member device without ctl3 (stored required grp1)
// stays waiting across release passes: release no longer queues what
// deliver parks again on every members push (CG-7). Once the device reads
// ctl3, it is handed over.
func TestGroupDeletionWaitsWithoutPingPong(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "group message to delete"})
	if err != nil {
		t.Fatal(err)
	}
	signCapsAfter(t, carol, without(ownCaps, protocol.CapControl))
	if _, err = w.alice.Retract(tctx(t), ControlRef{Conv: conv, ID: sent.LID, Fingerprint: w.alice.Self().Fingerprint()}, ""); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := w.alice.store.db.QueryRow(`SELECT id FROM outbox WHERE sub=? AND recipient=?`, envelope.SubRetraction, carol.Address).Scan(&id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "carol's copy waits", func() bool { _ = w.alice.FlushOutbox(tctx(t)); return outboxState(t, w.alice, id) == stateConvWaiting })
	counter := &profileCounter{base: w.alice.hub.http.Transport}
	w.alice.hub.http.Transport = counter
	feats, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		w.alice.releaseConv(tctx(t), feats)
		if st := outboxState(t, w.alice, id); st != stateConvWaiting {
			t.Fatalf("pass %d: release queued a copy deliver parks again: %s", i, st)
		}
		_ = w.alice.FlushOutbox(tctx(t))
	}
	signCapsAfter(t, carol, ownCaps)
	w.alice.releaseConv(tctx(t), feats)
	_ = w.alice.FlushOutbox(tctx(t))
	eventually(t, "handed over once carol reads ctl3", func() bool {
		st := outboxState(t, w.alice, id)
		return st == protocol.StateCustody || st == protocol.StateDelivered
	})
}

// A history copy kept as ciphertext only (empty body) while its reader
// lacked apx1 is released once the reader reads apx1: its requirement is
// the stored column, never a re-parsed body (CG-8).
func TestEmptyBodyHistoryCopyReleased(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	bob, _, found, err := w.alice.store.peer(w.bob.Address)
	if err != nil || !found {
		t.Fatalf("peer: %v %v", found, err)
	}
	id := protocol.NewID()
	if _, err := w.alice.store.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,created_ms,sub,required_cap,recipient_fp,error)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, w.bob.Address, "", `{"v":2}`, stateConvWaiting, time.Now().Unix(), conv, protocol.NewID(), envelope.KindMessage, time.Now().UnixMilli(), envelope.SubHistory, protocol.CapExternalParticipation, bob.Fingerprint(), WaitPeerUpdate+"older reader"); err != nil {
		t.Fatal(err)
	}
	signCapsAfter(t, w.bob, without(ownCaps, protocol.CapExternalParticipation)) // an older reader
	feats, err := w.alice.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	w.alice.releaseConv(tctx(t), feats)
	if st := outboxState(t, w.alice, id); st != stateConvWaiting {
		t.Fatalf("released to a reader without apx1: %s", st)
	}
	signCapsAfter(t, w.bob, ownCaps)
	w.alice.releaseConv(tctx(t), feats)
	if st := outboxState(t, w.alice, id); st != stateQueued {
		t.Fatalf("empty-body history copy still %s after its reader reads apx1", st)
	}
}
