package client

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestHistoryRedactedCoverageKeepsExactBinding(t *testing.T) {
	w, phone, conv, ids := historyCatchupFixture(t, 1)
	a := w.alice
	if _, e := a.historyCatchupPage(tctx(t), phone.Self()); e != nil {
		t.Fatal(e)
	}
	source := func() historySourceRow {
		t.Helper()
		r, e := a.historySourceRows(a.store.db, "dir='in' AND id=?", "id", 1, ids[0])
		if e != nil || len(r) != 1 {
			t.Fatalf("source count=%d error=%v", len(r), e)
		}
		return r[0]
	}
	original := source()
	groupHistoryResolvedRetraction(t, a, conv, original.in.LID, original.key)
	copy, e := a.prepareHistorySource(phone.Self(), source())
	if e != nil || copy == nil {
		t.Fatalf("prepare error=%v", e)
	}
	if ok, e := historyRedactedSourceCovered(a.store.db, phone.Self(), *copy); e != nil || !ok {
		t.Fatalf("exact redacted coverage=%t error=%v", ok, e)
	}
	var carrier, raw, state, hash string
	if e := a.store.db.QueryRow(`SELECT h.carrier,o.body,o.state,h.hash FROM history_copies h JOIN outbox o ON o.id=h.carrier WHERE h.recipient_fp=? AND h.conv=? AND h.author=? AND h.lid=?`, phone.Self().Fingerprint(), conv, original.key, original.in.LID).Scan(&carrier, &raw, &state, &hash); e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"logical-id", "changed-body", "question", "wrong-ref"} {
		t.Run(mode, func(t *testing.T) {
			bad := *copy
			var item HistoryItem
			if e := decodeStrict([]byte(bad.in.Body), &item); e != nil {
				t.Fatal(e)
			}
			switch mode {
			case "logical-id":
				item.LID = item.ID
			case "changed-body":
				item.Body = "changed content"
			case "question":
				item.Kind = envelope.KindQuestion
			case "wrong-ref":
				item.Sub = envelope.SubRevision
				item.Ref = &envelope.Ref{ID: original.in.ID, Fingerprint: original.key}
			}
			data, _ := json.Marshal(item)
			bad.in.Body = string(data)
			ok, e := historyRedactedSourceCovered(a.store.db, phone.Self(), bad)
			if ok {
				t.Fatal("changed source accepted")
			}
			_ = e
		})
	}
	for _, mode := range []string{"expired", "recipient-key", "wrong-sub", "changed-carrier", "changed-ledger"} {
		t.Run(mode, func(t *testing.T) {
			switch mode {
			case "expired":
				_, e = a.store.db.Exec(`UPDATE outbox SET state='expired' WHERE id=?`, carrier)
			case "recipient-key":
				_, e = a.store.db.Exec(`UPDATE outbox SET recipient_fp='other-key' WHERE id=?`, carrier)
			case "wrong-sub":
				_, e = a.store.db.Exec(`UPDATE outbox SET sub='other' WHERE id=?`, carrier)
			case "changed-carrier":
				var item HistoryItem
				_ = decodeStrict([]byte(raw), &item)
				item.Body = "different retained content"
				data, _ := json.Marshal(item)
				_, e = a.store.db.Exec(`UPDATE outbox SET body=? WHERE id=?`, string(data), carrier)
			case "changed-ledger":
				_, e = a.store.db.Exec(`UPDATE history_copies SET hash='different-hash' WHERE carrier=?`, carrier)
			}
			if e != nil {
				t.Fatal(e)
			}
			ok, checkErr := historyRedactedSourceCovered(a.store.db, phone.Self(), *copy)
			if ok {
				t.Fatal("unusable carrier or changed ledger counted")
			}
			if mode == "changed-ledger" && !errors.Is(checkErr, errHistoryCatchupConflict) {
				t.Fatal("changed ledger lost strict conflict")
			}
			if _, e = a.store.db.Exec(`UPDATE outbox SET sub='history',state=?,recipient_fp=?,body=? WHERE id=?`, state, phone.Self().Fingerprint(), raw, carrier); e != nil {
				t.Fatal(e)
			}
			if _, e = a.store.db.Exec(`UPDATE history_copies SET hash=? WHERE carrier=?`, hash, carrier); e != nil {
				t.Fatal(e)
			}
		})
	}
	if _, e = a.store.db.Exec(`DELETE FROM inbox WHERE sub='retraction' AND conv=? AND ref_id=? AND ref_fp=?`, conv, original.in.LID, original.key); e != nil {
		t.Fatal(e)
	}
	if ok, e := historyRedactedSourceCovered(a.store.db, phone.Self(), *copy); e != nil || ok {
		t.Fatalf("absent exact retraction coverage=%t error=%v", ok, e)
	}
}
