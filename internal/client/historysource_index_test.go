package client

import (
	"database/sql"
	"strings"
	"testing"
)

type recordedQuery struct {
	sql  string
	args []any
}

// queryRecorder records each Query it forwards: the test plans the exact SQL
// and arguments the source page ran.
type queryRecorder struct {
	*storeDB
	seen []recordedQuery
}

func (r *queryRecorder) Query(query string, args ...any) (*sql.Rows, error) {
	r.seen = append(r.seen, recordedQuery{query, args})
	return r.storeDB.Query(query, args...)
}

// A history page is picked from outbox_history_source alone. The table row
// keeps each envelope inline before conv, lid and sub: ranking from the table
// read every kept copy's envelope once per page (owner's laptop, November 27,
// 2026: ~50 copied messages a minute to a newly linked phone). A newest-page
// and an older-history page must both rank without touching that row.
func TestHistorySourcePageRanksFromCoveringIndex(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 3)
	a := w.alice
	// Copies of real turns to other devices, the bulk of a long-lived outbox.
	if _, err := a.store.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<2000) INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,sub,recipient_fp,created_ms) SELECT lower(hex(randomblob(16))),'other/dev','{}',hex(randomblob(2500)),'delivered',1,?,'lid-'||(n.x%40),'message','status','fp',n.x FROM n`, conv); err != nil {
		t.Fatal(err)
	}
	var outbox int64
	if err := a.store.db.QueryRow(`SELECT max(rowid) FROM outbox`).Scan(&outbox); err != nil {
		t.Fatal(err)
	}
	for _, page := range []struct {
		name, filter, order string
		args                []any
	}{
		{"newest", "conv=? AND arrival<=? AND outseq<=?", "ms DESC,id DESC", []any{conv, 1 << 40, outbox}},
		{"older", "arrival<=? AND outseq<=? AND (conv,ms,id)>(?,?,?)", "conv,ms,id", []any{1 << 40, outbox, "", 0, ""}},
	} {
		r := &queryRecorder{storeDB: a.store.db}
		rows, err := a.ownHistorySourceRows(r, phone.Self(), page.filter, page.order, historyPage, page.args...)
		if err != nil || len(rows) == 0 {
			t.Fatalf("%s page: rows=%d queries=%d err=%v", page.name, len(rows), len(r.seen), err)
		}
		var pick *recordedQuery
		for i := range r.seen {
			if strings.HasPrefix(r.seen[i].sql, "SELECT dir, id FROM") {
				pick = &r.seen[i]
			}
		}
		if pick == nil {
			t.Fatalf("%s page chose no rows from narrow columns", page.name)
		}
		plan, err := a.store.db.Query("EXPLAIN QUERY PLAN "+pick.sql, pick.args...)
		if err != nil {
			t.Fatal(err)
		}
		var steps []string
		for plan.Next() {
			var id, parent, unused int
			var detail string
			if err = plan.Scan(&id, &parent, &unused, &detail); err != nil {
				t.Fatal(err)
			}
			steps = append(steps, detail)
		}
		plan.Close()
		all := strings.Join(steps, "\n")
		if !strings.Contains(all, "COVERING INDEX outbox_history_source") || strings.Contains(all, "SCAN o\n") || strings.Contains(all, "SEARCH o USING INTEGER PRIMARY KEY") {
			t.Fatalf("%s page ranks from the outbox table row:\n%s", page.name, all)
		}
		t.Logf("%s page plan uses outbox_history_source", page.name)
	}
}
