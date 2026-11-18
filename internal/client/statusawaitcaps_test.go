package client

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A requester whose capability record is not on the relay yet (its daemon
// has not connected since it started) is not told nothing: the statuses stay
// due and reach it once its record arrives. Only a record without the
// capability means it cannot read them.
func TestStatusWaitsForRequesterCaps(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "what OS?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateAnswered)
	eventually(t, "the bot's statuses wait for the requester", func() bool {
		var due int
		w.bob.store.db.QueryRow(`SELECT status_due FROM inbox WHERE id=?`, q.ID).Scan(&due)
		return due > 0
	})
	runAgent(t, w.alice)
	eventually(t, "alice sees the bot's word once her record is there", func() bool {
		e := execOf(t, w.alice, q.ID)
		return e != nil && e.Host == w.bob.Address && e.State == envelope.StatusDone
	})
}
