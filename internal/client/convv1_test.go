package client

import (
	"errors"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// C2/C1: a DM message never continues in the older (version 1) format and
// never appears in a version 1 reply thread; a DM reply stays within its
// own DM, sent or received.
func TestDMStaysOutOfVersion1(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	c1, c2 := newDM(t, w.alice, w.bob), newDM(t, w.alice, w.bob)
	for conv, body := range map[string]string{c1: "deploy topic", c2: "budget topic"} {
		if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	v1, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "a plain message"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold both DMs and the plain message", func() bool {
		return len(convBodies(t, w.bob, c1)) == 1 && len(convBodies(t, w.bob, c2)) == 1 && inboxCount(t, w.bob, `id = ?`, v1.ID) == 1
	})
	in1, _ := w.bob.ConversationMessages(c1)
	in2, _ := w.bob.ConversationMessages(c2)
	dmID, otherDM := in1[0].ID, in2[0].ID

	// The older format refuses to continue a DM, before anything is stored.
	if err := w.bob.CheckReplyTo(dmID, w.alice.Address); !errors.Is(err, ErrConversationItem) {
		t.Fatalf("CheckReplyTo on a DM message: %v", err)
	}
	var before int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&before)
	if _, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "v1 reply to a DM message", ReplyTo: dmID}); !errors.Is(err, ErrConversationItem) {
		t.Fatalf("a version 1 reply to a DM message: %v", err)
	}
	var after int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox`).Scan(&after)
	if after != before {
		t.Fatal("the refused reply was stored")
	}
	if _, err := w.bob.Conversation(dmID, 0, 0); !errors.Is(err, ErrConversationItem) {
		t.Fatalf("the version 1 thread view of a DM message: %v", err)
	}

	// A version 1 message naming a DM message (as an older or faulty peer
	// could) is kept as version 1, and its thread does not reach into the DM.
	r, _ := w.bob.id.Public(w.bob.Address).Recipient()
	env, err := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: 1, Kind: envelope.KindMessage,
		Body: "old peer reply", ReplyTo: dmID}, w.alice.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	c, err := w.bob.Conversation(env.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range c.Messages {
		if m.ID == dmID || m.Body == "deploy topic" {
			t.Fatal("a version 1 thread shows a DM message")
		}
	}
	if th, err := w.bob.Conversation(v1.ID, 0, 0); err != nil || th.Total != 1 {
		t.Fatalf("the plain message's own thread: %+v %v", th, err)
	}

	// A DM reply stays within its DM: sending refuses another DM's message
	// or a version 1 one, and admission holds one that names them.
	for _, target := range []string{otherDM, v1.ID} {
		if _, err := w.bob.SendConv(tctx(t), c1, ConvOutgoing{Body: "x", ReplyTo: target}); err == nil {
			t.Fatalf("a DM reply to %s outside the DM was sent", target)
		}
	}
	if _, err := w.bob.SendConv(tctx(t), c1, ConvOutgoing{Body: "on deploy", ReplyTo: dmID}); err != nil {
		t.Fatalf("a reply within the DM: %v", err)
	}
	_, raw, _, _ := w.alice.store.conversation(c1)
	for _, target := range []string{otherDM, v1.ID} {
		cross := craft(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindMessage, Body: "cross link", Conv: c1, LID: protocol.NewID(), Root: raw, ReplyTo: target})
		if err := w.bob.verifyAndStore(tctx(t), cross); err != nil {
			t.Fatal(err)
		}
		if r := heldReason(t, w.bob, cross.ID); r != reasonInvalid {
			t.Fatalf("a received DM reply to %s outside its DM: %q", target, r)
		}
	}
}
