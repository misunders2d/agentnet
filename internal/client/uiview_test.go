package client

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func waitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("no change signalled after %s", what)
	}
}

func threadsWith(t *testing.T, a *Agent, peer string) map[string]ThreadSummary {
	t.Helper()
	ts, err := a.Threads()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ThreadSummary{}
	for _, s := range ts {
		if s.Peer == peer {
			out[s.ID] = s
		}
	}
	return out
}

// Threads are the reply-linked groups of one peer's messages; the change
// feed fires for the daemon's own writes and for writes made by another
// agentnet process on the same home.
func TestThreadsAndChangeFeed(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	alice := w.alice.Address

	_, ch := w.bob.Changed()
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "which port?\nsecond line"})
	if err != nil {
		t.Fatal(err)
	}
	waitClosed(t, "a question arrived", ch)
	m, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "unrelated note"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "two threads", func() bool { return len(threadsWith(t, w.bob, alice)) == 2 })
	ts := threadsWith(t, w.bob, alice)
	qt, mt := ts[q.ID], ts[m.ID]
	if qt.Title != "which port?" || qt.Review != 1 || qt.Unread != 1 || qt.Count != 1 || mt.Count != 1 || mt.Review != 0 {
		t.Fatalf("threads %+v", ts)
	}
	if !mt.LastAt.After(qt.LastAt) && !mt.LastAt.Equal(qt.LastAt) {
		t.Fatalf("order %v %v", qt.LastAt, mt.LastAt)
	}

	// A reply joins its thread; the question no longer waits for bob.
	if _, err := w.bob.Reply(tctx(t), q.ID, "8443"); err != nil {
		t.Fatal(err)
	}
	ts = threadsWith(t, w.bob, alice)
	if qt := ts[q.ID]; len(ts) != 2 || qt.Count != 2 || qt.Review != 0 || qt.Last != "8443" {
		t.Fatalf("after reply %+v", ts)
	}
	// Alice's copy: the question she sent waits until the answer arrives.
	if at := threadsWith(t, w.alice, w.bob.Address)[q.ID]; !at.Waiting {
		t.Fatalf("alice's question not waiting: %+v", at)
	}

	// Another process on bob's home marks the message read: the daemon's
	// feed hears it through the local socket, and the counter survives.
	seq, ch := w.bob.Changed()
	other, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.MarkRead([]string{m.ID}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, "another process wrote", ch)
	if now, _ := w.bob.Changed(); now <= seq {
		t.Fatalf("seq %d -> %d", seq, now)
	}
	if mt := threadsWith(t, w.bob, alice)[m.ID]; mt.Unread != 0 {
		t.Fatalf("still unread: %+v", mt)
	}
}
