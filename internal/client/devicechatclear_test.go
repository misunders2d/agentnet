package client

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// v011Caps is what a v0.11.13 own device advertises: ownCaps with rem2 in
// the slot rem3 now holds (byte-identical otherwise, checked by
// TestOwnCapsRem3OnlySwap).
func v011Caps() []string {
	caps := slices.Clone(ownCaps)
	caps[slices.Index(caps, protocol.CapReminderSyncV3)] = protocol.CapReminderSyncV2
	slices.Sort(caps)
	return caps
}

// blockedOn reports whether a lists person as blocked.
func blockedOn(t *testing.T, a *Agent, person string) bool {
	t.Helper()
	got, err := a.BlockedPersons()
	if err != nil {
		t.Fatal(err)
	}
	return slices.Contains(got, person)
}

// deviceTopics are a's non-notice device-thread topics with peer.
func deviceTopics(t *testing.T, a *Agent, peer string) int {
	t.Helper()
	ts, err := a.Threads()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range ts {
		if s.Peer == peer && !s.NoticeOnly {
			n++
		}
	}
	return n
}

// Deleting a device chat converges across the person's own devices
// (AGENTS.md: shared state follows each person): the other own device
// erases the same turns once it reads rem3, a v0.11.13 device (rem2 only)
// keeps it waiting until it updates while it still gets block sync as
// before, a duplicate history copy never brings it back, and the other
// person keeps every copy.
func TestDeviceChatDeletionOnAllOwnDevices(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	dumpClears(t, w.alice, phone)
	bob := w.bob.Address
	for _, text := range []string{"topic one", "topic two", "topic three"} {
		if _, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: text}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "three topics on both of alice's devices", func() bool {
		return deviceTopics(t, w.alice, bob) == 3 && deviceTopics(t, phone, bob) == 3
	})
	signCapsNow(t, phone, v011Caps()) // a v0.11.13 phone: rem2, no rem3
	ts, err := w.alice.Threads()
	if err != nil || len(ts) == 0 {
		t.Fatalf("threads: %v %v", ts, err)
	}
	done, err := w.alice.DeleteDeviceChat(bob, ts[0].ID)
	if err != nil || done.Erased != 3 || done.Devices != 1 {
		t.Fatalf("delete chat: %+v %v", done, err)
	}
	if n := deviceTopics(t, w.alice, bob); n != 0 {
		t.Fatalf("%d topics left here", n)
	}
	if n := len(queuedClears(t, w.alice, phone.Address)); n != 1 {
		t.Fatalf("deletion copies for the phone: %d", n)
	}
	if n := deviceTopics(t, phone, bob); n != 3 {
		t.Fatalf("a device that cannot read device chat deletions applied one: %d topics", n)
	}
	eventually(t, "the deletion for the rem2-only phone to be held", func() bool {
		return slices.Equal(clearStates(t, w.alice, phone.Address), []string{stateConvWaiting})
	})
	if n := deviceTopics(t, phone, bob); n != 3 {
		t.Fatalf("a rem2-only phone applied a device chat deletion: %d topics", n)
	}
	// A rem2-only peer is read exactly as before: block sync still reaches it.
	someone := protocol.NewID()
	if err := w.alice.SetBlocked(someone, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the rem2-only phone to get the block", func() bool { return blockedOn(t, phone, someone) })
	if err := w.alice.SetBlocked(someone, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the rem2-only phone to get the unblock", func() bool { return !blockedOn(t, phone, someone) })
	// The phone updates (rem3): its held copy goes out and is applied.
	signCapsNow(t, phone, ownCaps)
	eventually(t, "the phone to erase the chat too", func() bool { return deviceTopics(t, phone, bob) == 0 })
	// A duplicate history copy of an erased turn stays erased there.
	rows, err := w.alice.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND sub='device-history'`, phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	var copies []envelope.Envelope
	for rows.Next() {
		var raw string
		var env envelope.Envelope
		if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &env) == nil {
			copies = append(copies, env)
		}
	}
	rows.Close()
	if len(copies) == 0 {
		t.Fatal("no device-history copies to the phone")
	}
	for _, env := range copies {
		in, err := envelope.Open(env, phone.id, phone.Address, w.alice.Self())
		if err != nil {
			t.Fatal(err)
		}
		in.ID = protocol.NewID()
		phone.verifyAndStore(tctx(t), sealTo(t, w.alice, phone, in))
	}
	if n := deviceTopics(t, phone, bob); n != 0 {
		t.Fatalf("a duplicate history copy brought back %d topics", n)
	}
	if _, err := w.alice.deviceHistoryPage(phone.Self()); err != nil {
		t.Fatal(err)
	}
	if n := deviceTopics(t, phone, bob); n != 0 {
		t.Fatalf("the history copy brought back %d topics", n)
	}
	// Never another person's copies.
	if n := deviceTopics(t, w.bob, w.alice.Address); n != 3 {
		t.Fatalf("the other person's copy: %d topics", n)
	}
}

// clearStates are the outbox states of a's deletion copies to address.
func clearStates(t *testing.T, a *Agent, address string) []string {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT state FROM outbox WHERE recipient=? AND sub=? ORDER BY id`, address, envelope.SubClear)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// rem3 takes rem2's slot and nothing else changes: the record stays at the
// same names and within the byte budget (no limit raised).
func TestOwnCapsRem3OnlySwap(t *testing.T) {
	if slices.Contains(ownCaps, protocol.CapReminderSyncV2) || !slices.Contains(ownCaps, protocol.CapReminderSyncV3) || len(ownCaps)+1 > protocol.MaxAdvertisedCaps {
		t.Fatalf("own caps: %d %v", len(ownCaps), ownCaps)
	}
	rem3 := protocol.CapsRecord{Caps: []string{protocol.CapReminderSyncV3}}
	if !rem3.Reads(protocol.CapReminderSyncV2) || !rem3.Reads(protocol.CapReminderSync) || !rem3.Reads(protocol.CapDeviceClear) {
		t.Fatal("rem3 must read everything rem2 and rem1 did, and device chat deletions")
	}
	rem2 := protocol.CapsRecord{Caps: []string{protocol.CapReminderSyncV2}}
	if !rem2.Reads(protocol.CapReminderSyncV2) || !rem2.Reads(protocol.CapReminderSync) || rem2.Reads(protocol.CapDeviceClear) {
		t.Fatal("a rem2-only peer reads exactly what it read before, and no device chat deletion")
	}
}

// The other direction of the swap (an older own device sending to an
// updated one, which lists rem3 and no rem2): v0.11.13 asks Has(rem2) or
// Has(rem1), so it reads the updated device as reading neither. Its
// block-sync copy then waits, never dropped, and goes out once it reads the
// device again as a reader (blocksync.go is unchanged since v0.11.13, so this
// is the path such a device runs; releasing on its update uses this code).
func TestBlockSyncHeldForNonReaderThenSent(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	// The phone publishes its own record when it connects; a fixture record
	// signed before that is replaced by it (slow under -race).
	eventually(t, "the phone's own capability record", func() bool {
		phone.capsPub.mu.Lock()
		defer phone.capsPub.mu.Unlock()
		return phone.capsPub.published
	})
	signCapsNow(t, phone, slices.DeleteFunc(slices.Clone(ownCaps), func(c string) bool { return c == protocol.CapReminderSyncV3 })) // read as v0.11.13 reads a rem3-only record
	// Alice sees the new record once the relay's push drops her cached
	// profile; blocking before that sends with the old capabilities.
	eventually(t, "alice to see the phone read no block marks", func() bool {
		advertised, reads := w.alice.peerReads(tctx(t), phone.Address, protocol.CapReminderSyncV2)
		return advertised && !reads
	})
	someone := protocol.NewID()
	if err := w.alice.SetBlocked(someone, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the block-sync copy to wait", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=? AND state=?`, phone.Address, envelope.SubBlockSync, stateConvWaiting).Scan(&n)
		return n == 1
	})
	if blockedOn(t, phone, someone) {
		t.Fatal("a non-reader got the block")
	}
	signCapsNow(t, phone, ownCaps) // rem3 only
	eventually(t, "the held block to reach the rem3 device", func() bool { return blockedOn(t, phone, someone) })
}
