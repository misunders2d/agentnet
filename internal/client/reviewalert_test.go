package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func (n *notes) matching(sub string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := 0
	for _, b := range n.bodies {
		if strings.Contains(b, sub) {
			c++
		}
	}
	return c
}

// A person's DM question stays in review but never raises the legacy
// review notification or a review notice: it follows the DM's opt-in
// alerts (off: nothing; on: only the DM alert). A direct message's held
// question and a task for this device's agent awaiting the person keep
// both.
func TestDMTurnsFollowDMAlertsNotReviewNotifications(t *testing.T) {
	st := installAgentStub(t)
	old := alertGrace
	alertGrace = 300 * time.Millisecond
	t.Cleanup(func() { alertGrace = old })
	w, conv, _, _ := agentWorld(t)
	n := fakeNotify(w.bob)
	if err := w.bob.SetReviewTo(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	quiet := func(what string) {
		t.Helper()
		time.Sleep(time.Second)
		if n.count() != 0 || len(notices(t, w.alice, w.bob.Address)) != 0 {
			t.Fatalf("%s: %d notifications %q, %d review notices", what, n.count(), n.last(), len(notices(t, w.alice, w.bob.Address)))
		}
	}

	// Alerts off: a DM question is held for bob, listed for review, and
	// raises nothing.
	q, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "lunch at 1?"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ? AND state = ?`, q.ID, stateConvHeld) == 1 })
	quiet("a DM question with alerts off")
	if review, _ := w.bob.Review(); len(review) != 1 || review[0].ID != q.ID {
		t.Fatalf("the DM question is not in review: %+v", review)
	}

	// Alerts on: only the DM alert.
	if err := w.bob.SetAlertPrefs(AlertPrefs{Enabled: true,
		Senders: []protocol.NotifySender{{Address: w.alice.Address, Fingerprint: w.alice.id.Public(w.alice.Address).Fingerprint()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindTask, Body: "book a room"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the DM alert", func() bool { return n.count() == 1 })
	time.Sleep(700 * time.Millisecond)
	if n.count() != 1 || n.last() != "AgentNet: New activity" || len(notices(t, w.alice, w.bob.Address)) != 0 {
		t.Fatalf("with alerts on: %d notifications, last %q, %d review notices", n.count(), n.last(), len(notices(t, w.alice, w.bob.Address)))
	}

	// A direct message's held question: the review notification and notice.
	legacy, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "the budget?"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the review notification", func() bool { return n.matching("need") == 1 })
	eventually(t, "the review notice", func() bool { return len(notices(t, w.alice, w.bob.Address)) == 1 })
	if !reviewSent(t, w.bob, legacy.ID) || reviewSent(t, w.bob, q.ID) {
		t.Fatal("the notice covers the wrong items")
	}

	// A task for bob's agent that needs bob's say: both again.
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	task, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "restart the deploy")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the task to wait for bob", func() bool { return jobState(t, w.bob, task.ID) == stateAwaiting })
	eventually(t, "its review notification", func() bool { return n.matching("need") == 2 })
	eventually(t, "its review notice", func() bool { return reviewSent(t, w.bob, task.ID) })
}
