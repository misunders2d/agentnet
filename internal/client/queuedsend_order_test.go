package client

import (
	"fmt"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// A burst accepted within one wall-clock step (Windows' clock moves in steps,
// not nanoseconds) shares one created_at: it still lists and leaves in the
// order accepted, never in the random order of its ids.
func TestQueuedSendsSameTickKeepAcceptedOrder(t *testing.T) {
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	runAgent(t, w.bob)
	conv := newDM(t, w.alice, w.bob)
	q := &w.alice.queued
	q.Lock()
	q.closed = true // accepted only; nothing drains them meanwhile
	q.Unlock()
	ids := make([]string, 40)
	for i := range ids {
		ids[i] = protocol.NewID()
		if _, err := w.alice.QueueSend(ScheduledSendInput{ID: ids[i], Conv: conv, Body: fmt.Sprintf("tick %d", i)}, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.alice.store.db.Exec(`UPDATE scheduled_sends SET created_at=(SELECT min(created_at) FROM scheduled_sends WHERE conv=?) WHERE conv=?`, conv, conv); err != nil {
		t.Fatal(err)
	}
	check := func(what string, got []ScheduledSend, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(what, err)
		}
		for i, s := range got {
			if s.ID != ids[i] {
				t.Fatalf("%s: order changed at %d", what, i)
			}
		}
	}
	listed, err := w.alice.QueuedSends(conv)
	if len(listed) != len(ids) {
		t.Fatalf("listed %d of %d: %v", len(listed), len(ids), err)
	}
	check("listed", listed, err)
	next, err := w.alice.nextQueuedSends(conv)
	if len(next) != len(ids) {
		t.Fatalf("next %d of %d: %v", len(next), len(ids), err)
	}
	check("next to send", next, err)
}
