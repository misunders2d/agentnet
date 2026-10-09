package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestHistoryCatchupPreservesOriginalGroupMetadataAndRevision(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	a, conv := w.alice, packet.State.Conv
	p6Member(t, a, w.bob, conv)
	phone, awaited, _ := linkPhone(t, a, "source-metadata-phone")
	request := pendingLink(t, a)
	stops[a]()
	if err := a.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	sent, err := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "original topic message", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := a.RefOf(conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Revise(tctx(t), ref, "visible revised topic message"); err != nil {
		t.Fatal(err)
	}
	rows, err := a.historySourceRows(a.store.db, "conv=? AND lid=? AND dir='out'", "conv,ms,id", 1, conv, sent.LID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("original source: %d %v", len(rows), err)
	}
	original, err := a.historySourceItem(a.store.db, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if original.Topic == "" || original.Human == nil {
		t.Fatalf("fixture lacks signed metadata: topic=%t audience=%t", original.Topic != "", original.Human != nil)
	}
	selected, err := a.groupHistorySources(a.store.db, conv, sent.LID, a.Self().Fingerprint(), 0, 2)
	if err != nil || len(selected) != 1 {
		t.Fatalf("selected source: %d %v", len(selected), err)
	}
	legacy := HistoryItem{V: 1, Kind: envelope.KindMessage, From: original.From, FromKey: original.FromKey, ID: original.ID, LID: original.LID, TS: original.TS, At: original.At, Body: "visible revised topic message", ReplyTo: original.ReplyTo, Origin: original.Origin, Emotion: original.Emotion, ReceiverRoute: original.ReceiverRoute, Attachments: original.Attachments}
	if historyRef(conv, selected[0].item) != historyRef(conv, legacy) {
		t.Fatal("existing signed selected projection changed")
	}
	for pages := 0; ; pages++ {
		more, e := a.historyCatchupPage(tctx(t), phone.Self())
		if e != nil || pages > 10 {
			t.Fatalf("catch-up: %d %v", pages, e)
		}
		if !more {
			break
		}
	}
	var found, edit bool
	for _, item := range historyCopiedItems(t, a, phone) {
		if item.LID == sent.LID {
			if historyRef(conv, item) != historyRef(conv, original) || item.Body != original.Body {
				t.Fatal("own original was projected or lost signed metadata")
			}
			if item.Topic != original.Topic || item.Human == nil {
				t.Fatal("own original topic/audience missing")
			}
			found = true
		}
		if item.Sub == envelope.SubRevision && item.Ref != nil && item.Ref.ID == sent.LID {
			var change envelope.Revision
			if json.Unmarshal([]byte(item.Body), &change) != nil || change.Text != "visible revised topic message" {
				t.Fatal("separate revision changed")
			}
			edit = true
		}
	}
	if !found || !edit {
		t.Fatalf("original/revision separate copies: %v/%v", found, edit)
	}
}

func TestHistoryCatchupIntentionalExclusionDoesNotStayDeferred(t *testing.T) {
	_, a, phone, packet := groupHistoryLinkedFixture(t)
	sent, err := a.SendConv(tctx(t), packet.State.Conv, ConvOutgoing{Body: "then deliberately retracted"})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := a.RefOf(packet.State.Conv, sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Retract(tctx(t), ref, "removed by its author"); err != nil {
		t.Fatal(err)
	}
	rows, err := a.historySourceRows(a.store.db, "dir='out' AND conv=? AND lid=?", "conv,ms,id", 1, packet.State.Conv, sent.LID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("retained source: %d %v", len(rows), err)
	}
	if copy, err := a.prepareHistorySource(phone.Self(), rows[0]); err != nil || copy != nil {
		t.Fatalf("source must be deliberately excluded: %v %v", copy != nil, err)
	}
	if _, err = a.store.db.Exec(`INSERT INTO history_deferred(recipient_fp,dir,id) VALUES(?,'out',?)`, phone.Self().Fingerprint(), rows[0].in.ID); err != nil {
		t.Fatal(err)
	}
	for pages := 0; ; pages++ {
		more, e := a.historyCatchupPage(tctx(t), phone.Self())
		if e != nil || pages > 10 {
			t.Fatalf("catch-up: %d %v", pages, e)
		}
		if !more {
			break
		}
	}
	var n int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM history_deferred WHERE recipient_fp=? AND dir='out' AND id=?`, phone.Self().Fingerprint(), rows[0].in.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("intentional exclusion remained deferred: %d %v", n, err)
	}
	for _, item := range historyCopiedItems(t, a, phone) {
		if item.LID == sent.LID {
			t.Fatal("retracted original copied")
		}
	}
}
