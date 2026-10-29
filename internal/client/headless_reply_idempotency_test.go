package client

import (
	"reflect"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestOperatorReportReplyQueuedRetryKeepsExactDecision(t *testing.T) {
	w := newWorld(t, "")
	stopSender, stopHost := runAgent(t, w.alice), runAgent(t, w.bob)
	if _, err := w.bob.Send(tctx(t), w.alice.Address, "Pin the operator", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.GrantOperator(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "Which region?"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, q.ID, stateHeld)
	var report Message
	var item ReportItem
	eventually(t, "operator report names the held request", func() bool {
		for _, m := range mustNotices(t, w.alice) {
			if r, ok := w.alice.NoticeReport(m); ok {
				for _, it := range r.Items {
					if it.ID == q.ID {
						report, item = m, it
						return true
					}
				}
			}
		}
		return false
	})
	stopSender()
	stopHost()
	sender := w.alice
	id := protocol.NewID()
	decide := func(body string) (ControlSent, error) {
		return sender.Decide(WithQueuedSend(tctx(t), id), w.bob.Address, item.ID, item.Key, "reply", item.State, item.Attempt, body, report.ID)
	}
	first, err := decide("Use eu-west")
	if err != nil || first.ID != id {
		t.Fatal(first, err)
	}
	original, err := w.alice.store.outboxEnvelope(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	sender, err = Open(sender.home)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	second, err := decide("Use eu-west")
	if err != nil || second.ID != id {
		t.Fatalf("same exact report reply retry must reuse its durable decision: %+v %v", second, err)
	}
	if _, err := decide("Changed reply must be refused"); err == nil {
		t.Fatal("same ID accepted a different reply")
	}
	after, err := sender.store.outboxEnvelope(id)
	if err != nil || !reflect.DeepEqual(after, original) {
		t.Fatal("retry or changed body replaced the original sealed decision", err)
	}
	for range 2 {
		if err := w.bob.verifyAndStore(tctx(t), original); err != nil {
			t.Fatal(err)
		}
	}
	var replies int
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE reply_to=? AND json_extract(envelope,'$.kind')=?`, q.ID, envelope.KindAnswer).Scan(&replies); err != nil || replies != 1 {
		t.Fatalf("retried decision must answer once: %d %v", replies, err)
	}
}
