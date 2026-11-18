package ui

import (
	"net/http"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// delayRelay makes every request of agent wait d at the Hub, as on a far relay.
func delayRelay(t *testing.T, hub interface{ SetHold(func(*http.Request)) }, agent string, d time.Duration) {
	t.Helper()
	hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == agent {
			time.Sleep(d)
		}
	})
	t.Cleanup(func() { hub.SetHold(nil) })
}

// S6d: the first message of a new topic is queued like any other: accepted
// at once with a slow relay, sent once, in one new topic, also when sent again.
func TestNewTopicFirstMessageIsQueuedAtOnce(t *testing.T) {
	bob, live, hub, conv, _ := queuedSendWorld(t)
	delayRelay(t, hub, bob.Address, 2*time.Second)
	id, started := protocol.NewID(), time.Now()
	sent, err := live.SendDM(DMDraft{ID: id, Conv: conv, Topic: "new", Body: "opens a topic"})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took >= 1100*time.Millisecond {
		t.Fatalf("a new topic's first message took %v to accept", took)
	}
	if sent.LID != id || sent.State != "queued" {
		t.Fatalf("accepted as %+v", sent)
	}
	row, found, err := bob.ScheduledSend(id)
	if err != nil || !found || !protocol.ValidID(row.Topic) {
		t.Fatalf("queued without its own topic: %+v %v %v", row, found, err)
	}
	if again, err := live.SendDM(DMDraft{ID: id, Conv: conv, Topic: "new", Body: "opens a topic"}); err != nil || again.LID != id {
		t.Fatalf("sent again: %+v %v", again, err)
	}
	hub.SetHold(nil)
	storedSend(t, live, conv, sent)
	thread, err := live.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, m := range thread.Messages {
		if m.LID == id {
			n++
			if m.Topic != row.Topic {
				t.Fatalf("sent in topic %q, accepted in %q", m.Topic, row.Topic)
			}
		}
	}
	if n != 1 {
		t.Fatalf("%d copies of the message shown", n)
	}
}

// S7 (owner, 2026-11-18): with the relay up, a sent message is in the outbox,
// where it can be edited, about a second after Send, even with every relay
// request taking 150 ms.
func TestSentMessageEditableWithinASecond(t *testing.T) {
	bob, live, hub, conv, _ := queuedSendWorld(t)
	delayRelay(t, hub, bob.Address, 150*time.Millisecond)
	var worst time.Duration
	for i := range 3 {
		started := time.Now()
		sent, err := live.SendDM(DMDraft{ID: protocol.NewID(), Conv: conv, Body: "editable soon"})
		if err != nil {
			t.Fatal(err)
		}
		storedSend(t, live, conv, sent)
		took := time.Since(started)
		worst = max(worst, took)
		t.Logf("SEND_TO_EDITABLE i=%d ms=%d", i, took.Milliseconds())
	}
	if worst >= 1100*time.Millisecond {
		t.Fatalf("send to editable took up to %v", worst)
	}
}

// Delete while pending cancels the send, as in Telegram (TDLib
// cancel_send_message_query): Discard of a queued new-topic first message
// wins over its attempt, and nothing is stored or sent.
func TestDiscardCancelsQueuedNewTopicSend(t *testing.T) {
	bob, live, hub, conv, _ := queuedSendWorld(t)
	delayRelay(t, hub, bob.Address, 2*time.Second)
	id := protocol.NewID()
	if _, err := live.SendDM(DMDraft{ID: id, Conv: conv, Topic: "new", Body: "never sent"}); err != nil {
		t.Fatal(err)
	}
	if row, err := bob.DiscardQueuedSend(id); err != nil || row.State != client.ScheduledCancelled {
		t.Fatalf("discard: %+v %v", row, err)
	}
	hub.SetHold(nil)
	time.Sleep(3 * time.Second) // a held attempt finishes or gives up
	if copies, err := bob.SentCopies(id); err != nil || len(copies) != 0 {
		t.Fatalf("a discarded message was stored: %+v %v", copies, err)
	}
}
