package client

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryCatchupPreservesPIDTopicAndFileSource(t *testing.T) {
	w, _, packet, stops := groupTurnsFixture(t)
	a, conv := w.alice, packet.State.Conv
	part := p6Member(t, a, w.bob, conv)
	phone, awaited, _ := linkPhone(t, a, "pid-topic-phone")
	request := pendingLink(t, a)
	stops[a]()
	if err := a.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("exact source attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	sent, err := a.AskAgentInTopic(tctx(t), part.PID, envelope.KindQuestion, "question with topic", "new", nil, OutgoingFile{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := a.historySourceRows(a.store.db, "conv=? AND lid=? AND dir='out'", "conv,ms,id", 1, conv, sent.LID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("source rows: %d %v", len(rows), err)
	}
	original, err := a.historySourceItem(a.store.db, rows[0])
	if err != nil {
		t.Fatal(err)
	}
	if original.Topic == "" || original.Human == nil || len(original.Attachments) != 1 {
		t.Fatal("fixture missing full source metadata")
	}
	if _, err = a.groupParticipationSourceAdmission(a.store.db, packet, original); err != nil {
		t.Fatalf("exact original topic source refused: %v", err)
	}
	sources, err := a.groupParticipationFileSources(a.store.db, conv, sent.LID, a.Self().Fingerprint(), 16)
	if err != nil || len(sources) < 1 {
		t.Fatalf("exact topic file source: %d %v", len(sources), err)
	}
	for _, source := range sources {
		if historyRef(conv, source.item) != historyRef(conv, original) || source.item.Topic != original.Topic || len(source.item.Attachments) != 1 {
			t.Fatal("file source metadata lost or changed")
		}
	}
	changed := original
	changed.Topic = protocol.NewID()
	if _, err = a.groupParticipationSourceAdmission(a.store.db, packet, changed); err == nil {
		t.Fatal("different topic accepted as same exact source")
	}
	for pages := 0; ; pages++ {
		more, e := a.historyCatchupPage(tctx(t), phone.Self())
		if e != nil || pages > 20 {
			t.Fatalf("catch-up: %d %v", pages, e)
		}
		if !more {
			break
		}
	}
	found := false
	for _, item := range historyCopiedItems(t, a, phone) {
		if item.LID == original.LID {
			if historyRef(conv, item) != historyRef(conv, original) {
				t.Fatal("queued topic source hash differs")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("topic request remained deferred")
	}
}
