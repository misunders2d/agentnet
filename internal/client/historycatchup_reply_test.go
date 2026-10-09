package client

import (
	"errors"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryCatchupMissingReplyTargetStillCopiesVerifiedMessage(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 1)
	a := w.alice
	_, root := rootOf(t, w.bob, conv)
	parent := protocol.NewID()
	in := envelope.Inner{Kind: envelope.KindMessage, Body: "reply whose older target is absent here", Conv: conv, Root: root, LID: protocol.NewID(), ReplyTo: parent, Origin: envelope.OriginUI}
	env := craft(t, w.bob, a, in)
	if err := a.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	for page := 0; ; page++ {
		more, err := a.historyCatchupPage(tctx(t), phone.Self())
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
		if page >= 10 {
			t.Fatal("unbounded catch-up")
		}
	}
	var found bool
	for _, item := range historyCopiedItems(t, a, phone) {
		if item.ID == env.ID {
			found = item.Body == in.Body && item.ReplyTo == parent
		}
	}
	if !found {
		t.Fatal("verified reply blocked forever by an unavailable older parent")
	}
	var deferred int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM history_deferred WHERE recipient_fp=?`, phone.Self().Fingerprint()).Scan(&deferred); err != nil || deferred != 0 {
		t.Fatalf("reply remained deferred: %d %v", deferred, err)
	}
	for _, kind := range []string{envelope.KindAnswer, envelope.KindResult} {
		row := historySourceRow{conv: conv, in: envelope.Inner{Kind: kind, ReplyTo: parent}}
		if deps, err := a.historyDependencies(row, HistoryItem{}); err != nil || len(deps) != 0 {
			t.Fatalf("missing reply ancestry is not an admission dependency: %s %v", kind, err)
		}
	}
	control := historySourceRow{conv: conv, in: envelope.Inner{Sub: envelope.SubStatus, Ref: &envelope.Ref{ID: parent, Fingerprint: w.bob.Self().Fingerprint()}}}
	if _, err := a.historyDependencies(control, HistoryItem{}); !errors.Is(err, ErrGroupContextPending) {
		t.Fatalf("missing exact control dependency lost its guard: %v", err)
	}
}
