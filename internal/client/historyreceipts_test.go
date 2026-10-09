package client

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestHistoryCarrierReceiptSurvivesRestartWithoutVisibleTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.db.Close() }()
	carrier := protocol.NewID()
	in := envelope.Inner{ID: protocol.NewID(), LID: protocol.NewID(), Conv: "conversation", From: "alice/old", Kind: envelope.KindMessage, Body: "retained inert original", TS: 1}
	denied := errors.New("synthetic authority changed")
	if _, err = s.addHistoryInbox(in, 1, "author-key", "alice/linked", carrier, false, func(*sql.Tx) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("admission refusal: %v", err)
	}
	if seen, err := s.seen(carrier); err != nil || seen {
		t.Fatalf("refused transaction kept success: %v %v", seen, err)
	}
	if err = s.holdAs(envelope.Envelope{ID: carrier, From: "alice/linked"}, reasonProof); err != nil {
		t.Fatal(err)
	}
	if _, err = s.addHistoryInbox(in, 1, "author-key", "alice/linked", carrier, true, nil); err != nil {
		t.Fatal(err)
	}
	s.db.Close()
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := s.disposition(carrier); err != nil || state != protocol.StateDelivered {
		t.Fatalf("restart disposition: %q %v", state, err)
	}
	if seen, err := s.seen(carrier); err != nil || !seen {
		t.Fatalf("carrier dedup lost: %v %v", seen, err)
	}
	rows, err := s.unsentReceipts()
	if err != nil || len(rows) != 1 || rows[0] != (receipt{carrier, protocol.StateDelivered}) {
		t.Fatalf("exact carrier receipt: %+v %v", rows, err)
	}
	if err = s.markAcked(rows[0]); err != nil {
		t.Fatal(err)
	}
	if rows, err = s.unsentReceipts(); err != nil || len(rows) != 0 {
		t.Fatalf("ack did not drain: %+v %v", rows, err)
	}
	if err = s.resendReceipt(carrier); err != nil {
		t.Fatal(err)
	}
	if rows, err = s.unsentReceipts(); err != nil || len(rows) != 1 || rows[0].id != carrier {
		t.Fatalf("duplicate did not reack: %+v %v", rows, err)
	}
	var visible, work, held int
	if err = s.db.QueryRow(`SELECT count(*),sum(CASE WHEN state!='' OR acked!=1 THEN 1 ELSE 0 END),(SELECT count(*) FROM quarantine) FROM inbox`).Scan(&visible, &work, &held); err != nil || visible != 1 || work != 0 || held != 0 {
		t.Fatalf("receipt created a turn/work or retained obsolete hold: %d %d %d %v", visible, work, held, err)
	}
}
