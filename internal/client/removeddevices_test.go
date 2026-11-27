package client

import (
	"slices"
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
		// The phone is this person's own: the own list agrees exactly.
		own, err := w.alice.OwnRemovedDevices()
		if err != nil || slices.Contains(own, phone.Address) != want || slices.Contains(own, w.bob.Address) {
			t.Fatalf("own removed devices: %v %v; want removed=%v", own, err, want)
		}
	}
	check(false)
	if err = w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
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
