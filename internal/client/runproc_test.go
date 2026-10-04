package client

import (
	"errors"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// BUG-09: with no daemon running, nothing runs a request, so cancel refuses
// and says why, instead of reporting success and recording a cancel that
// nothing acts on; the request stays as it was. With the daemon running,
// cancel is taken as before.
func TestCancelRefusedWithoutDaemon(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	stopBob := runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "is it running?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds it", func() bool { return inboxCount(t, w.bob, `id = ?`, q.ID) == 1 })
	stopBob()
	// As a daemon that died while it ran leaves it.
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, stateRunning, q.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Cancel(q.ID); !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("cancel with no daemon: %v", err)
	}
	if s, _ := w.bob.store.jobState(q.ID); s != stateRunning {
		t.Fatalf("a refused cancel changed the request to %s", s)
	}
	// The daemon starts: the request is interrupted, not cancelled.
	runAgent(t, w.bob)
	waitState(t, w.bob, q.ID, stateInterrupt)
}
