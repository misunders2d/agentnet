package client

import (
	"strings"
	"testing"
)

// BUG-10: a sent DM message is shown with the id of the other member's
// copy, whatever order the copies' ids sort in, never the copy to one of
// the sender's own devices; each copy says whether it went to one of
// them. Its logical id lists every copy sent here.
func TestSentDMShowsPeerCopy(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.alice, w.bob)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "two copies"})
	if err != nil {
		t.Fatal(err)
	}
	// Ids are random: make the phone's copy sort first, as it does about
	// half the time.
	low, high := strings.Repeat("0", 32), strings.Repeat("f", 32)
	for _, c := range sent.Copies {
		id := high
		if c.To == phone.Address {
			id = low
		}
		if _, err := w.alice.store.db.Exec(`UPDATE outbox SET id = ? WHERE id = ?`, id, c.ID); err != nil {
			t.Fatal(err)
		}
	}
	msgs, err := w.alice.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != high || len(msgs[0].Copies) != 2 {
		t.Fatalf("shown %+v", msgs)
	}
	for _, c := range msgs[0].Copies {
		if c.Own != (c.To == phone.Address) {
			t.Fatalf("copy %+v", c)
		}
	}
	copies, err := w.alice.SentCopies(sent.LID)
	if err != nil || len(copies) != 2 || copies[0].ID+copies[1].ID != high+low && copies[0].ID+copies[1].ID != low+high {
		t.Fatalf("copies of the logical id: %+v %v", copies, err)
	}
	if copies, err := w.alice.SentCopies(high); err != nil || len(copies) != 0 {
		t.Fatalf("a copy id is not a logical id: %+v %v", copies, err)
	}
}
