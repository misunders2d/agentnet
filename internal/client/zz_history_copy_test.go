package client

import (
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Tests for copying history to a newly linked device in large parts. They
// live in a late file so adding them does not move earlier tests between
// race shards.

// A part is packed when it is full, or when nothing is in flight; up to
// HistoryArchiveWindow parts may be in flight to one device at once.
func TestHistoryArchiveWindowPipelinesFullParts(t *testing.T) {
	_, a, phone, _ := archiveFixture(t, 60)
	for range 6 {
		if _, err := a.pageByHand(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	fp := phone.Self().Fingerprint()
	var staged int
	var template string
	if err := a.store.db.QueryRow(`SELECT count(*),min(id) FROM outbox WHERE state=? AND recipient_fp=?`, archiveStaged, fp).Scan(&staged, &template); err != nil || staged < 1 {
		t.Fatalf("no staged copies: %d %v", staged, err)
	}
	inFlight := func(n int) {
		t.Helper()
		for range n {
			if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,sub,recipient_fp) VALUES(lower(hex(randomblob(16))),?,'','{}',?,1,?,?)`, phone.Address, protocol.StateCustody, envelope.SubHistoryArchive, fp); err != nil {
				t.Fatal(err)
			}
		}
	}
	parts := func() (n, largest int) {
		t.Helper()
		if err := a.store.db.QueryRow(`SELECT count(*),coalesce(max(c),0) FROM (SELECT count(*) c FROM history_archive_entries GROUP BY manifest)`).Scan(&n, &largest); err != nil {
			t.Fatal(err)
		}
		return n, largest
	}
	pack := func() {
		t.Helper()
		if err := a.archivePack(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	// One part in flight: a partial part waits for more copies.
	inFlight(1)
	pack()
	if n, _ := parts(); n != 0 {
		t.Fatalf("packed a partial part while one was in flight: %d", n)
	}
	// Fill exactly one full part from the staged copy (distinct IDs; packing
	// checks structure only, it never opens or re-signs a copy).
	if _, err := a.store.db.Exec(`WITH RECURSIVE n(x,id) AS (SELECT 1,lower(hex(randomblob(16))) UNION ALL SELECT x+1,lower(hex(randomblob(16))) FROM n WHERE x<?)
 INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,sub,recipient_fp,created_ms,required_cap,mention_cap,organization_cap,shared_title_cap,resources_cap)
 SELECT n.id,o.recipient,o.body,json_set(o.envelope,'$.id',n.id),o.state,o.created_at,o.conv,o.lid,o.kind,o.sub,o.recipient_fp,o.created_ms,o.required_cap,o.mention_cap,o.organization_cap,o.shared_title_cap,o.resources_cap FROM n JOIN outbox o ON o.id=?`, protocol.MaxHistoryArchiveEntries-staged, template); err != nil {
		t.Fatal(err)
	}
	// The window is full: even a full part waits.
	inFlight(protocol.HistoryArchiveWindow - 1)
	pack()
	if n, _ := parts(); n != 0 {
		t.Fatalf("packed past the window of %d: %d", protocol.HistoryArchiveWindow, n)
	}
	// One slot frees: the full part goes while the others are still in flight.
	if _, err := a.store.db.Exec(`DELETE FROM outbox WHERE rowid=(SELECT min(rowid) FROM outbox WHERE sub=? AND envelope='{}')`, envelope.SubHistoryArchive); err != nil {
		t.Fatal(err)
	}
	pack()
	if n, largest := parts(); n != 1 || largest != protocol.MaxHistoryArchiveEntries {
		t.Fatalf("full part did not pipeline behind %d in flight: parts=%d largest=%d", protocol.HistoryArchiveWindow-1, n, largest)
	}
	var live int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=? AND recipient_fp=? AND state IN `+archiveInFlight, envelope.SubHistoryArchive, fp).Scan(&live); err != nil || live != protocol.HistoryArchiveWindow {
		t.Fatalf("in flight after packing: %d %v", live, err)
	}
}

// A crash part-way through a part resumes at the next copy: nothing is
// admitted twice and nothing is lost.
func TestHistoryArchivePartResumesMidPartWithoutDuplicates(t *testing.T) {
	_, a, phone, conv := archiveFixture(t, 40)
	for range 12 {
		if _, err := a.pageByHand(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.archivePack(tctx(t)); err != nil {
		t.Fatal(err)
	}
	desc := archiveDescriptor(t, a)
	if _, err := a.archiveStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var entries int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM history_archive_entries WHERE manifest=?`, desc.ID).Scan(&entries); err != nil || entries < 20 {
		t.Fatalf("part too small to interrupt: %d %v", entries, err)
	}
	if err := phone.storeReceived(tctx(t), desc); err != nil {
		t.Fatal(err)
	}
	// The process dies storing the 11th copy, after its archive row.
	if _, err := phone.store.db.Exec(`CREATE TRIGGER test_crash_mid_part BEFORE INSERT ON inbox WHEN (SELECT count(*) FROM history_archive_children) > 10 BEGIN SELECT RAISE(ABORT,'synthetic crash'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.archiveImportStep(tctx(t)); err == nil {
		t.Fatal("import ran past the crash")
	}
	var pos int
	if err := phone.store.db.QueryRow(`SELECT pos FROM history_archive_jobs WHERE id=?`, desc.ID).Scan(&pos); err != nil || pos != 10 {
		t.Fatalf("checkpoint after crash: pos=%d %v", pos, err)
	}
	if _, err := phone.store.db.Exec(`DROP TRIGGER test_crash_mid_part; UPDATE history_archive_jobs SET error='' WHERE id=?`, desc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.archiveImportStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if more, err := phone.archiveImportStep(tctx(t)); err != nil || more {
		t.Fatalf("completed part ran again: %v %v", more, err)
	}
	var done, total, distinct, children int
	if err := phone.store.db.QueryRow(`SELECT pos,done FROM history_archive_jobs WHERE id=?`, desc.ID).Scan(&pos, &done); err != nil || pos != entries || done != 1 {
		t.Fatalf("resumed job: pos=%d/%d done=%d %v", pos, entries, done, err)
	}
	if err := phone.store.db.QueryRow(`SELECT count(*),count(DISTINCT coalesce(lid,id)) FROM inbox WHERE conv=? AND body LIKE 'row-%'`, conv).Scan(&total, &distinct); err != nil {
		t.Fatal(err)
	}
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM history_archive_children WHERE manifest=?`, desc.ID).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if total != distinct || children != entries {
		t.Fatalf("resume duplicated or lost copies: rows=%d distinct=%d children=%d entries=%d", total, distinct, children, entries)
	}
}

// Each side of a history page takes its own first page before the merge.
// That must pick exactly the first page of the whole ordering: here, every
// page shape over interleaved received and sent rows (sent duplicates by
// lid collapse to their first copy) against the same pick with no limit.
// The received side must walk its index, never scan and sort the inbox.
func TestHistorySourcePagePerSideLimitMatchesWholeOrder(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 40)
	a := w.alice
	if _, err := a.store.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<60) INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,sub,recipient_fp,created_ms) SELECT lower(hex(randomblob(16))),'other/dev','{}','{}','delivered',1,?,'lid-'||(n.x%45),'message','status','fp',995+n.x FROM n`, conv); err != nil {
		t.Fatal(err)
	}
	var outbox, arrival int64
	if err := a.store.db.QueryRow(`SELECT max(rowid) FROM outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	if err := a.store.db.QueryRow(`SELECT CAST(v AS INTEGER) FROM config WHERE k='arrival'`).Scan(&arrival); err != nil {
		t.Fatal(err)
	}
	ids := func(rows []historySourceRow) []string {
		var out []string
		for _, r := range rows {
			out = append(out, r.dir+"/"+r.in.ID)
		}
		return out
	}
	all, err := a.ownHistorySourceRows(a.store.db, phone.Self(), "conv=? AND arrival<=? AND outseq<=?", "ms DESC,id DESC", 1000, conv, arrival, outbox)
	if err != nil || len(all) < 80 {
		t.Fatalf("fixture rows: %d %v", len(all), err)
	}
	mid := all[len(all)/2]
	var firstArrival int64
	a.store.db.QueryRow(`SELECT min(arrival) FROM inbox WHERE conv=?`, conv).Scan(&firstArrival)
	for _, page := range []struct {
		name, filter, order, index string
		args                       []any
	}{
		{"newest", "conv=? AND arrival<=? AND outseq<=?", "ms DESC,id DESC", "inbox_history_pick", []any{conv, arrival, outbox}},
		{"older", "arrival<=? AND outseq<=? AND (conv,ms,id)>(?,?,?)", "conv,ms,id", "inbox_history_pick", []any{arrival, outbox, "", 0, ""}},
		{"older from a cursor", "arrival<=? AND outseq<=? AND (conv,ms,id)>(?,?,?)", "conv,ms,id", "inbox_history_pick", []any{arrival, outbox, mid.pos.Conv, mid.pos.Ms, mid.pos.ID}},
		{"tail", "dir='in' AND arrival>? AND arrival<=?", "arrival", "inbox_history_arrival", []any{firstArrival + 5, arrival}},
	} {
		whole, err := a.ownHistorySourceRows(a.store.db, phone.Self(), page.filter, page.order, 1000, page.args...)
		if err != nil || len(whole) < 20 {
			t.Fatalf("%s: whole order %d %v", page.name, len(whole), err)
		}
		for _, limit := range []int{1, 7, historyPage} {
			r := &queryRecorder{storeDB: a.store.db}
			got, err := a.ownHistorySourceRows(r, phone.Self(), page.filter, page.order, limit, page.args...)
			if err != nil {
				t.Fatal(err)
			}
			want := ids(whole[:min(limit, len(whole))])
			if strings.Join(ids(got), ",") != strings.Join(want, ",") {
				t.Fatalf("%s limit %d picked %v, want %v", page.name, limit, ids(got), want)
			}
			if limit != historyPage {
				continue
			}
			for _, q := range r.seen {
				if !strings.HasPrefix(q.sql, "SELECT dir, id FROM") {
					continue
				}
				plan, err := a.store.db.Query("EXPLAIN QUERY PLAN "+q.sql, q.args...)
				if err != nil {
					t.Fatal(err)
				}
				var steps []string
				for plan.Next() {
					var id, parent, unused int
					var detail string
					plan.Scan(&id, &parent, &unused, &detail)
					steps = append(steps, detail)
				}
				plan.Close()
				joined := strings.Join(steps, "\n")
				if !strings.Contains(joined, "INDEX "+page.index) || strings.Contains(joined, "SCAN inbox") {
					t.Fatalf("%s page does not walk %s:\n%s", page.name, page.index, joined)
				}
			}
		}
	}
}
