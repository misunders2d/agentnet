package client

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// sentView is the message with logical id lid in conv as a reloaded view
// lists it, and its copy to address.
func sentView(t *testing.T, a *Agent, conv, lid, address string) (ConvMessage, ConvCopy) {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs {
		if m.LID == lid && m.Dir == "out" {
			for _, c := range m.Copies {
				if c.To == address {
					return m, c
				}
			}
			t.Fatalf("%s not listed with the message: %+v", address, m.Copies)
		}
	}
	t.Fatalf("message %s not listed", lid)
	return ConvMessage{}, ConvCopy{}
}

// One member device whose key cannot be used (changed and not trusted
// here, or removed by a Hub admin) gets nothing, and only it: the group's
// turns and state changes still reach everyone else (CG-13). Before, one
// such device stopped every member's group send and rename. The message
// keeps saying so: that device is listed as not sent, with why, and its
// delivery never reads as everyone's.
func TestGroupTurnSkipsOneUnusableDevice(t *testing.T) {
	w, carol, packet, _ := groupTurnsFixture(t)
	conv := packet.State.Conv
	has := func(a *Agent, body string) func() bool {
		return func() bool { return slices.Contains(convBodies(t, a, conv), "in:"+body) }
	}

	// Carol's key changed as Alice sees it: never used until trusted.
	other, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	trusted, _, _, err := w.alice.store.peer(carol.Address)
	if err != nil {
		t.Fatal(err)
	}
	wrong, _ := json.Marshal(other.Public(carol.Address))
	if _, err := w.alice.store.db.Exec(`UPDATE peers SET public = ? WHERE address = ?`, string(wrong), carol.Address); err != nil {
		t.Fatal(err)
	}
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "carol's key changed"})
	if err != nil {
		t.Fatalf("one device's changed key stopped the group turn: %v", err)
	}
	var skipped bool
	for _, c := range sent.Copies {
		if c.To == carol.Address {
			skipped = c.State == stateNotDelivered && c.NotSent && strings.Contains(c.Detail, "key")
		}
	}
	if !skipped || sent.State != stateNotDelivered || !strings.Contains(sent.Detail, "key") {
		t.Fatalf("carol's device not named as not sent: %s %q %+v", sent.State, sent.Detail, sent.Copies)
	}
	var stored int
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=?`, sent.LID, carol.Address).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("a copy was sealed for the untrusted key: %d %v", stored, err)
	}
	eventually(t, "bob has alice's turn", has(w.bob, "carol's key changed"))
	eventually(t, "bob's copy delivered", func() bool {
		_, c := sentView(t, w.alice, conv, sent.LID, w.bob.Address)
		return c.State == protocol.StateDelivered
	})
	m, c := sentView(t, w.alice, conv, sent.LID, carol.Address)
	if m.Delivery != stateNotDelivered || m.State != stateNotDelivered || !c.NotSent || c.State != stateNotDelivered || !strings.Contains(c.Detail, "key") || c.Person == "" {
		t.Fatalf("stored view reads as delivered past a member that got nothing: delivery %q state %q %+v", m.Delivery, m.State, m.Copies)
	}
	copies, err := w.alice.SentCopies(sent.LID)
	if err != nil || !slices.ContainsFunc(copies, func(c ConvCopy) bool { return c.To == carol.Address && c.NotSent }) {
		t.Fatalf("status lists no record for carol's device: %+v %v", copies, err)
	}
	if _, err := w.alice.RenameGroup(tctx(t), conv, "Renamed past a changed key"); err != nil {
		t.Fatalf("one device's changed key stopped a rename: %v", err)
	}
	eventually(t, "bob sees the rename", func() bool {
		c, e := w.bob.GroupContext(conv)
		return e == nil && c.State.Title == "Renamed past a changed key"
	})
	raw, _ := json.Marshal(trusted)
	if _, err := w.alice.store.db.Exec(`UPDATE peers SET public = ?, pending = NULL WHERE address = ?`, string(raw), carol.Address); err != nil {
		t.Fatal(err)
	}

	// Carol's only device removed by a Hub admin: the others still talk.
	if err := w.alice.Revoke(tctx(t), carol.Address); err != nil {
		t.Fatal(err)
	}
	removed, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "after carol was removed"})
	if err != nil {
		t.Fatalf("one revoked device stopped the group turn: %v", err)
	}
	eventually(t, "alice has bob's turn", has(w.alice, "after carol was removed"))
	if m, c := sentView(t, w.bob, conv, removed.LID, carol.Address); m.Delivery != stateNotDelivered || !c.NotSent {
		t.Fatalf("bob's view reads as delivered past carol's removed device: %q %+v", m.Delivery, m.Copies)
	}
}
