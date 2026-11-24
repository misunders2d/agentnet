//go:build android || agentnet_sqlite_cgo

package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

func TestCGOReadOnlyCommitErrorCleansConnection(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "commit.db"), []string{
		`CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED);`,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// ReadOnly is the driver's lock-mode hint, as in modernc. Force a
	// deferred constraint failure to exercise database/sql's commit cleanup.
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO child VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("invalid deferred constraint committed")
	}
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM child`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("failed commit left an open transaction: rows=%d err=%v", rows, err)
	}
	next, err := db.Begin()
	if err != nil {
		t.Fatalf("connection not reusable after failed commit: %v", err)
	}
	if err := next.Rollback(); err != nil {
		t.Fatal(err)
	}
}

// Run with -tags agentnet_sqlite_cgo on a CGo-enabled host to exercise the
// actual Android adapter and the existing schema/backup suite. Android runtime
// qualification still requires running the packaged app under Android.
func TestCGOSQLiteStorePreservesPragmasAndJSON(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "adapter.db"), v1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var journal string
	if err = db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal=%q error=%v", journal, err)
	}
	for _, tc := range []struct {
		pragma string
		want   int
	}{{"foreign_keys", 1}, {"busy_timeout", 5000}, {"synchronous", 2}} {
		var got int
		if err = db.QueryRow("PRAGMA " + tc.pragma).Scan(&got); err != nil || got != tc.want {
			t.Fatalf("%s=%d,want%d error=%v", tc.pragma, got, tc.want, err)
		}
	}
	var field string
	if err = db.QueryRow(`SELECT json_extract('{"field":"supported"}', '$.field')`).Scan(&field); err != nil || field != "supported" {
		t.Fatalf("JSON projection=%q error=%v", field, err)
	}
}
func TestCGOSQLiteBusyPrimaryCodesOnly(t *testing.T) {
	for _, code := range []sqlite3.ErrNo{sqlite3.ErrBusy, sqlite3.ErrLocked} {
		err := sqlite3.Error{Code: code, ExtendedCode: sqlite3.ErrNoExtended(int(code) | 2<<8)}
		if !Busy(err) || !Busy(fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("primary SQLite lock code not recognized:%v", err)
		}
	}
	for _, err := range []error{nil, errors.New("database is locked"), sqlite3.Error{Code: sqlite3.ErrConstraint}} {
		if Busy(err) {
			t.Fatalf("nonlock error treated asbusy:%v", err)
		}
	}
}

func TestCGOSQLiteFullResult(t *testing.T) {
	if !Full(fmt.Errorf("writing: %w", sqlite3.Error{Code: sqlite3.ErrFull})) {
		t.Fatal("wrapped SQLite capacity result was not recognized")
	}
	if Full(sqlite3.Error{Code: sqlite3.ErrConstraint}) {
		t.Fatal("SQLite constraint result was mistaken for a capacity error")
	}
}
