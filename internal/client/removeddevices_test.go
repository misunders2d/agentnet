package client

import (
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
)

func TestRemovedDevicesExactCurrentKey(t *testing.T) {
	w, _, _ := selfConsentWorld(t)
	phone := linkedVia(t, w.alice, "removed-phone", approveLink(w.alice))
	// Pin through ordinary signed delivery, then use the normal removal path.
	sent, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "current device"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone pin", func() bool { return inboxHas(t, w.alice, sent.ID) })
	check := func(want bool) {
		t.Helper()
		got, err := w.alice.RemovedDevices()
		if err != nil || slices.Contains(got, phone.Address) != want || slices.Contains(got, w.bob.Address) {
			t.Fatalf("removed peers: %v %v; want removed=%v", got, err, want)
		}
		// The phone is this person's own: its owner is named exactly.
		me, _, _ := w.alice.Person()
		owned, err := w.alice.RemovedDeviceOwners()
		if err != nil || slices.Contains(owned, RemovedDevice{Address: phone.Address, Person: me.Person}) != want || len(owned) != len(got) {
			t.Fatalf("removed device owners: %v %v; want removed=%v", owned, err, want)
		}
	}
	check(false)
	keeper := func(peer, want string) {
		t.Helper()
		if got, err := w.alice.DeviceChatKeeper(peer); err != nil || got != want {
			t.Fatalf("keeper of %s: %q %v; want %q", peer, got, err, want)
		}
	}
	keeper(phone.Address, "") // another current device of this person: it gets the deletion
	if err = w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	check(true)
	keeper(phone.Address, "Your removed device is not told.")
	// Re-enrolled: a current roster names the address again (under a key
	// this device has not pinned yet). It is not a removed device.
	me, _, _ := w.alice.Person()
	if _, err = w.alice.store.db.Exec(`INSERT INTO person_devices(address,person,fingerprint,added) VALUES(?,?,?,1)`, phone.Address, me.Person, "00000000-0000-0000-0000-000000000000"); err != nil {
		t.Fatal(err)
	}
	check(false)
	if _, err = w.alice.store.db.Exec(`DELETE FROM person_devices WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	check(true)
	// Its device thread stays searchable history (GET /api/topics).
	page, err := w.alice.Topics(TopicQuery{Query: "current device"})
	if err != nil || !slices.ContainsFunc(page.Topics, func(s ThreadSummary) bool { return s.Peer == phone.Address }) {
		t.Fatalf("removed device's thread not found by search: %+v %v", page.Topics, err)
	}
	newKey, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	// A newly observed key remains an identity decision, never a removed label.
	if err = w.alice.store.setPending(newKey.Public(phone.Address)); err != nil {
		t.Fatal(err)
	}
	check(false)
	// After normal pinning, the same address can be a standalone service. Its
	// new exact key never belonged to the historical roster.
	if err = w.alice.store.pin(newKey.Public(phone.Address)); err != nil {
		t.Fatal(err)
	}
	check(false)
	if err = w.alice.store.pin(phone.Self()); err != nil {
		t.Fatal(err)
	}
	check(true)
	replacement := linkedVia(t, w.alice, "replacement-phone", approveLink(w.alice))
	got, err := w.alice.RemovedDevices()
	if err != nil || slices.Contains(got, replacement.Address) {
		t.Fatalf("replacement hidden: %v %v", got, err)
	}
}

// Another verified person's removed device (owner 2026-11-27, "same as
// mine"): the exact key their signed roster dropped is named with that
// person; its thread stays searchable history.
func TestRemovedDeviceOfAnotherPerson(t *testing.T) {
	w, _, _ := selfConsentWorld(t)
	phone := linkedVia(t, w.bob, "bobs-phone", approveLink(w.bob))
	sent, err := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "from bobs phone"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone pin", func() bool { return inboxHas(t, w.alice, sent.ID) })
	bob, _, _ := w.bob.Person()
	if _, err := w.alice.refreshPerson(tctx(t), bob.Person, false); err != nil { // alice knows the phone in Bob's roster
		t.Fatal(err)
	}
	if owned, err := w.alice.RemovedDeviceOwners(); err != nil || len(owned) != 0 {
		t.Fatalf("a current device is not removed: %v %v", owned, err)
	}
	if err = w.bob.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.refreshPerson(tctx(t), bob.Person, false); err != nil {
		t.Fatal(err)
	}
	owned, err := w.alice.RemovedDeviceOwners()
	if err != nil || !slices.Equal(owned, []RemovedDevice{{Address: phone.Address, Person: bob.Person}}) {
		t.Fatalf("bob's removed phone: %v %v", owned, err)
	}
	if got, err := w.alice.DeviceChatKeeper(phone.Address); err != nil || got != "That removed device keeps its copy." {
		t.Fatalf("keeper of bob's removed phone: %q %v", got, err)
	}
	if got, err := w.alice.DeviceChatKeeper(w.bob.Address); err != nil || !strings.HasSuffix(got, " keeps their copy.") || got == " keeps their copy." {
		t.Fatalf("keeper of bob's laptop: %q %v", got, err)
	}
	if got, err := w.alice.DeviceChatKeeper("nobody/unknown"); err != nil || got != "That device keeps its copy." {
		t.Fatalf("keeper of an unknown device: %q %v", got, err)
	}
	page, err := w.alice.Topics(TopicQuery{Query: "bobs phone"})
	if err != nil || !slices.ContainsFunc(page.Topics, func(s ThreadSummary) bool { return s.Peer == phone.Address }) {
		t.Fatalf("removed device's thread not found by search: %+v %v", page.Topics, err)
	}
}
