package sqlitedb

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestReadOnlySnapshotAllowsConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.db")
	steps := []string{`CREATE TABLE snapshot_value(value TEXT); INSERT INTO snapshot_value VALUES('old')`}
	reader, err := Open(path, steps)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	writer, err := Open(path, steps)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`PRAGMA busy_timeout=100`); err != nil {
		t.Fatal(err)
	}
	tx, err := reader.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var value string
	if err := tx.QueryRow(`SELECT value FROM snapshot_value`).Scan(&value); err != nil || value != "old" {
		t.Fatalf("initial snapshot: value=%q err=%v", value, err)
	}
	if _, err := writer.Exec(`UPDATE snapshot_value SET value='new'`); err != nil {
		t.Fatalf("read-only snapshot blocked another writer: %v", err)
	}
	if err := tx.QueryRow(`SELECT value FROM snapshot_value`).Scan(&value); err != nil || value != "old" {
		t.Fatalf("snapshot changed while held: value=%q err=%v", value, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow(`SELECT value FROM snapshot_value`).Scan(&value); err != nil || value != "new" {
		t.Fatalf("fresh read missed write: value=%q err=%v", value, err)
	}
}

func TestReaderReadsBesideAHeldWriteAndRefusesWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	writer, err := Open(path, []string{`CREATE TABLE v(value TEXT); INSERT INTO v VALUES('old')`})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reader, err := OpenReader(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := writer.Begin() // immediate: holds the write lock
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE v SET value='new'`); err != nil {
		t.Fatal(err)
	}
	var value string
	if err := reader.QueryRow(`SELECT value FROM v`).Scan(&value); err != nil || value != "old" {
		t.Fatalf("read beside a held write: value=%q err=%v", value, err)
	}
	if _, err := reader.Exec(`INSERT INTO v VALUES('x')`); err == nil {
		t.Fatal("the reader wrote")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow(`SELECT value FROM v`).Scan(&value); err != nil || value != "new" {
		t.Fatalf("committed write unseen: value=%q err=%v", value, err)
	}
}
