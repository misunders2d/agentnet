package client

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func quarantinedBlocked(t *testing.T, a *Agent) (held, shown int) {
	t.Helper()
	if err := a.store.db.QueryRow(`SELECT count(*),coalesce(sum(notice_archived=0),0) FROM quarantine WHERE reason=?`, reasonBlocked).Scan(&held, &shown); err != nil {
		t.Fatal(err)
	}
	return held, shown
}

// Blocking a person holds their direct messages quietly on every own device;
// unblocking lets new ones through and never releases what was held.
func TestBlockDirectMessagesAcrossOwnDevices(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	bob, _, err := w.bob.store.selfPerson(w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	send := func(body string) {
		t.Helper()
		if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	send("before the block")
	eventually(t, "first message on both own devices", func() bool {
		return len(convBodies(t, w.alice, conv)) == 1 && len(convBodies(t, phone, conv)) == 1
	})
	if err := w.alice.SetBlocked(bob.info.Person, true); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.SetBlocked(func() string { me, _, _ := w.alice.store.selfPerson(w.alice.Address); return me.info.Person }(), true); err != errBlockSelf {
		t.Fatalf("self block: %v", err)
	}
	eventually(t, "block follows own human device", func() bool {
		b, err := phone.BlockedPersons()
		return err == nil && slices.Equal(b, []string{bob.info.Person})
	})
	send("while blocked")
	eventually(t, "held quietly on both own devices", func() bool {
		a, _ := quarantinedBlocked(t, w.alice)
		p, _ := quarantinedBlocked(t, phone)
		return a == 1 && p == 1
	})
	for _, d := range []*Agent{w.alice, phone} {
		if _, shown := quarantinedBlocked(t, d); shown != 0 {
			t.Fatal("a blocked message was offered as a held notice")
		}
		if got := convBodies(t, d, conv); len(got) != 1 {
			t.Fatalf("blocked message reached the chat: %v", got)
		}
	}
	if b, _ := w.bob.BlockedPersons(); len(b) != 0 {
		t.Fatalf("block leaked to the blocked person: %v", b)
	}
	if err := phone.SetBlocked(bob.info.Person, false); err != nil {
		t.Fatal(err)
	}
	eventually(t, "unblock follows own human device", func() bool {
		b, err := w.alice.BlockedPersons()
		return err == nil && len(b) == 0
	})
	send("after unblocking")
	eventually(t, "new messages arrive again", func() bool {
		return len(convBodies(t, w.alice, conv)) == 2 && len(convBodies(t, phone, conv)) == 2
	})
	for _, d := range []*Agent{w.alice, phone} {
		if got := convBodies(t, d, conv); slices.Contains(got, "while blocked") {
			t.Fatalf("unblocking released a held message: %v", got)
		}
	}
}

// Block marks come only from current own human devices; an older mark never
// replaces a newer one.
func TestBlockSyncAuthorityAndOrder(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	me, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	target := protocol.NewID()
	receive := func(sender *Agent, m protocol.BlockMark) {
		t.Helper()
		raw, _ := json.Marshal(protocol.BlockSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Blocks: []protocol.BlockMark{m}})
		env := craft(t, sender, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubBlockSync, Replica: true, Body: string(raw)})
		if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	receive(w.bob, protocol.BlockMark{Person: target, Blocked: true, Revision: now, Writer: w.bob.Self().Fingerprint()})
	if b, _ := w.alice.BlockedPersons(); len(b) != 0 {
		t.Fatalf("another person's device set a block: %v", b)
	}
	receive(phone, protocol.BlockMark{Person: target, Blocked: true, Revision: now + 2, Writer: phone.Self().Fingerprint()})
	receive(phone, protocol.BlockMark{Person: target, Blocked: false, Revision: now + 1, Writer: phone.Self().Fingerprint()})
	if b, _ := w.alice.BlockedPersons(); !slices.Equal(b, []string{target}) {
		t.Fatalf("own newer block, then a stale unblock: %v", b)
	}
}
