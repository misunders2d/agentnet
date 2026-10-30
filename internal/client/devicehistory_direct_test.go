package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An own device that is an original's exact direct recipient receives the
// original itself; a device-history copy only queued the same item twice
// behind it (the phone got each answer and status twice). It still gets a
// copy when this device's own send state says it missed the original.
func TestDeviceHistorySkipsOriginalsTheRecipientHolds(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	send := func(to *Agent, body string) string {
		t.Helper()
		sent, err := a.SendMessage(tctx(t), Outgoing{To: to.Address, Kind: envelope.KindQuestion, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return sent.ID
	}
	direct, missed, other := send(phone, "to the phone itself"), send(phone, "to the phone, expired"), send(w.bob, "to bob")
	setState := func(id, state string) {
		t.Helper()
		if _, err := a.store.db.Exec(`UPDATE outbox SET state=? WHERE id=?`, state, id); err != nil {
			t.Fatal(err)
		}
	}
	setState(direct, protocol.StateCustody)
	setState(missed, protocol.StateExpired)
	page := func() {
		t.Helper()
		for range 4 {
			a.convWork.mu.Lock()
			a.convWork.historyDeferred = nil // an evidence wake: the pending sweep runs again
			a.convWork.mu.Unlock()
			if _, err := a.deviceHistoryPage(phone.Self()); err != nil {
				t.Fatal(err)
			}
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := a.store.db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	copied := func(id string) int {
		return count(`SELECT count(*) FROM device_history_copies c JOIN outbox o ON o.id=c.carrier WHERE c.id=? AND c.recipient_fp=? AND o.recipient=? AND o.sub=?`, id, phone.Self().Fingerprint(), phone.Address, envelope.SubDeviceHistory)
	}
	pending := func(id string) int {
		return count(`SELECT count(*) FROM device_history_pending WHERE recipient_fp=? AND storage='out' AND id=?`, phone.Self().Fingerprint(), id)
	}
	page()
	if n := copied(direct); n != 0 || pending(direct) != 1 {
		t.Fatalf("original in transit to its own recipient: %d copies, pending %d", n, pending(direct))
	}
	if copied(missed) != 1 {
		t.Fatal("an expired original lost its device-history copy")
	}
	if copied(other) != 1 {
		t.Fatal("another recipient's original lost its device-history copy")
	}
	setState(direct, protocol.StateDelivered)
	page()
	if n := copied(direct); n != 0 || pending(direct) != 0 {
		t.Fatalf("delivered original: %d copies, pending %d", n, pending(direct))
	}
	// A later miss of a still-pending original is covered by the sweep.
	late := send(phone, "to the phone, later not delivered")
	setState(late, protocol.StateCustody)
	page()
	if copied(late) != 0 || pending(late) != 1 {
		t.Fatal("in-transit original was copied or not kept pending")
	}
	setState(late, stateNotDelivered)
	page()
	if copied(late) != 1 || pending(late) != 0 {
		t.Fatalf("missed original: %d copies, pending %d", copied(late), pending(late))
	}
}

// An original deleted after its copy reached an own device differs from
// that copy, but the device holds it: the deletion follows as its own copy.
// Comparing first stopped every own device's history sync at that page
// (the live store: nothing past one deleted message since October 20).
func TestDeviceHistoryDeletionAfterDeliveredCopy(t *testing.T) {
	w, phone, _, _ := historyCatchupFixture(t, 0)
	a := w.alice
	sent, err := a.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "oops, wrong chat"})
	if err != nil {
		t.Fatal(err)
	}
	page := func() {
		t.Helper()
		for range 4 {
			a.convWork.mu.Lock()
			a.convWork.historyDeferred = nil
			a.convWork.mu.Unlock()
			if _, err := a.deviceHistoryPage(phone.Self()); err != nil {
				t.Fatal(err)
			}
		}
	}
	copied := func(id string) int {
		t.Helper()
		var n int
		if err := a.store.db.QueryRow(`SELECT count(*) FROM device_history_copies c JOIN outbox o ON o.id=c.carrier WHERE c.id=? AND c.recipient_fp=? AND o.sub=?`, id, phone.Self().Fingerprint(), envelope.SubDeviceHistory).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	page()
	if copied(sent.ID) != 1 {
		t.Fatal("the original was not copied")
	}
	if _, err := a.store.db.Exec(`UPDATE outbox SET state=? WHERE id IN (SELECT carrier FROM device_history_copies WHERE id=?)`, protocol.StateDelivered, sent.ID); err != nil {
		t.Fatal(err)
	}
	ref, err := a.RefOf("", sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	gone, err := a.Retract(tctx(t), ref, "")
	if err != nil {
		t.Fatal(err)
	}
	page()
	if copied(gone.ID) != 1 {
		t.Fatal("the deletion was not copied to the device holding the original")
	}
}
