package client

import (
	"path/filepath"
	"testing"
)

func continuityToken(t *testing.T, q dbq) string {
	t.Helper()
	var token string
	if err := q.QueryRow(`SELECT token FROM continuity_generation`).Scan(&token); err != nil {
		t.Fatal(err)
	}
	return token
}

// MEL-748: history catch-up froze the app because every proof check of every
// item parsed and verified every pinned roster again. The result may be reused
// only while its exact inputs are unchanged.
func TestContinuityTokenFollowsExactInputs(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	token := continuityToken(t, s.db)
	for _, change := range []struct {
		name, sql string
		changes   bool
	}{
		{"person added", `INSERT INTO persons(person,label,seq,hash,record,state,pinned_at) VALUES('p','L',0,'r','{}','pinned',1)`, true},
		{"person state", `UPDATE persons SET state='conflict' WHERE person='p'`, true},
		{"person conflict", `UPDATE persons SET conflict='x' WHERE person='p'`, true},
		{"continuity head", `INSERT OR REPLACE INTO config(k,v) VALUES('continuity-head/p','h')`, true},
		{"continuity head changed", `INSERT OR REPLACE INTO config(k,v) VALUES('continuity-head/p','h2')`, true},
		{"continuity head updated", `UPDATE config SET v='h3' WHERE k='continuity-head/p'`, true},
		{"continuity head removed", `DELETE FROM config WHERE k='continuity-head/p'`, true},
		{"revoked device", `INSERT OR REPLACE INTO config(k,v) VALUES('revoked-exact-device/a/f','a')`, true},
		{"revoked device removed", `DELETE FROM config WHERE k='revoked-exact-device/a/f'`, true},
		{"person removed", `DELETE FROM persons WHERE person='p'`, true},
		{"unrelated config", `INSERT OR REPLACE INTO config(k,v) VALUES('arrival','7')`, false},
	} {
		if _, err := s.db.Exec(change.sql); err != nil {
			t.Fatalf("%s: %v", change.name, err)
		}
		next := continuityToken(t, s.db)
		if (next != token) != change.changes {
			t.Fatalf("%s: token %s -> %s, want changed=%v", change.name, token, next, change.changes)
		}
		token = next
	}
}

func TestLogicalPersonsSharedAcrossCallersAndTransactions(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	if _, err := s.db.Exec(`INSERT INTO config(k,v) VALUES('continuity-head/p','h')`); err != nil {
		t.Fatal(err)
	}
	resolves := func(f func()) int64 {
		t.Helper()
		before := continuityResolves.Load()
		f()
		return continuityResolves.Load() - before
	}
	ask := func(q dbq) {
		t.Helper()
		if _, err := logicalPersons(q); err != nil {
			t.Fatal(err)
		}
	}
	if n := resolves(func() { ask(s.db); ask(s); ask(s.db) }); n != 1 {
		t.Fatalf("first state computed %d times across *sql.DB and *store callers", n)
	}
	if _, err := s.db.Exec(`INSERT OR REPLACE INTO config(k,v) VALUES('arrival','9')`); err != nil {
		t.Fatal(err)
	}
	if n := resolves(func() { ask(s.db) }); n != 0 {
		t.Fatalf("an unrelated write discarded the result: %d", n)
	}
	before := continuityToken(t, s.db)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if n := resolves(func() { ask(tx) }); n != 0 {
		t.Fatalf("an unchanged transaction computed again: %d", n)
	}
	if _, err := tx.Exec(`INSERT INTO persons(person,label,seq,hash,record,state,pinned_at) VALUES('p','L',0,'r','not json','pinned',1)`); err != nil {
		t.Fatal(err)
	}
	inTx := continuityToken(t, tx)
	if inTx == before {
		t.Fatal("a transaction's own write kept the old token")
	}
	// Its own uncommitted write is read, so the invalid record is seen here.
	if _, err := logicalPersons(tx); err == nil {
		t.Fatal("a transaction read a result older than its own write")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if after := continuityToken(t, s.db); after != before {
		t.Fatalf("rollback left token %s, want %s", after, before)
	}
	if n := resolves(func() { ask(s.db) }); n != 0 {
		t.Fatalf("rollback lost the unchanged result: %d", n)
	}
}
