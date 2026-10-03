package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// BUG-29: messages that arrive within one second are listed as they
// arrived, not by their random ids.
func TestInboxListsSameSecondArrivalsInOrder(t *testing.T) {
	w := newWorld(t, "")
	// Descending ids: listing by id would reverse them.
	ids := []string{strings.Repeat("f", 32), strings.Repeat("e", 32), strings.Repeat("d", 32), strings.Repeat("c", 32)}
	for i, id := range ids {
		in := envelope.Inner{ID: id, From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "seq-" + string(rune('1'+i))}
		if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	// All in one second; the last two carry milliseconds, as conversation
	// messages do.
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET received_at = 1000, received_ms = CASE id WHEN ? THEN 1000500 WHEN ? THEN 1000900 END`, ids[2], ids[3]); err != nil {
		t.Fatal(err)
	}
	msgs, err := w.bob.Inbox(false, false)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Body)
	}
	if strings.Join(got, " ") != "seq-1 seq-2 seq-3 seq-4" {
		t.Fatalf("inbox order %v", got)
	}
}
