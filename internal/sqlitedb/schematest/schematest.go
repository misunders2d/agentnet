// Package schematest is the fixture around sqlitedb's fresh image, for test packages
// whose subject is not the schema, and the proof that a store started from that image
// is the store running every schema step would have made. Its results say nothing about
// schema steps themselves: those run one by one, wherever a package does not call Use.
package schematest

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

// perStore holds the one row a schema step means to differ between stores: the token
// that keys what this process may remember about a store's people.
const perStore = "continuity_generation"

// Use turns on sqlitedb's fresh image for a test process.
func Use() { sqlitedb.ReuseFreshSchema(true, again) }

// Copies counts the stores that started from the image, so a run can say so.
func Copies() int64 { return copies.Load() }

var copies atomic.Int64

func again(db *sql.DB) error {
	copies.Add(1)
	var there int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='table' AND name=?`, perStore).Scan(&there); err != nil || there == 0 {
		return err
	}
	// Replacing the row, as the step inserted it: an update would count as a write to the list's inputs.
	_, err := db.Exec(`INSERT OR REPLACE INTO ` + perStore + ` VALUES(1,hex(randomblob(16)))`)
	return err
}

// CheckFreshImage compares a store that ran steps one by one with one started from the
// image, row by row, and proves two copies of the image do not share the per-store row.
func CheckFreshImage(t *testing.T, steps []string) {
	t.Helper()
	t.Cleanup(func() { sqlitedb.ReuseFreshSchema(false, nil) })
	open := func(name string) (contents, token string) {
		t.Helper()
		db, err := sqlitedb.Open(filepath.Join(t.TempDir(), name), steps)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		return dump(t, db)
	}
	sqlitedb.ReuseFreshSchema(false, nil)
	byStep, _ := open("steps.db")
	Use()
	open("remembers.db")
	fromImage, token := open("image.db")
	if fromImage != byStep {
		t.Fatalf("a store from the image differs from one that ran every step:\n%s", firstDifference(byStep, fromImage))
	}
	if _, other := open("image2.db"); token != "" && token == other {
		t.Fatal("two stores from the image share their per-store token")
	}
}

func firstDifference(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := range al {
		if i >= len(bl) || al[i] != bl[i] {
			other := "(ended)"
			if i < len(bl) {
				other = bl[i]
			}
			return fmt.Sprintf("line %d\n by step: %s\n image:   %s", i+1, al[i], other)
		}
	}
	return fmt.Sprintf("image has %d more lines", len(bl)-len(al))
}

// dump is everything a new store holds: settings, schema, and every row but the per-store one.
func dump(t *testing.T, db *sql.DB) (contents, token string) {
	t.Helper()
	var out strings.Builder
	for _, pragma := range []string{"user_version", "application_id", "journal_mode", "foreign_keys", "integrity_check", "auto_vacuum", "encoding", "page_size", "synchronous"} {
		var v string
		if err := db.QueryRow("PRAGMA " + pragma).Scan(&v); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s=%s\n", pragma, v)
	}
	rows, err := db.Query(`SELECT type,name,tbl_name,coalesce(sql,'') FROM sqlite_schema ORDER BY type,name`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var kind, name, table, text string
		if err := rows.Scan(&kind, &name, &table, &text); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&out, "%s %s %s %s\n", kind, name, table, text)
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		lines, err := tableRows(db, table)
		if err != nil {
			t.Fatal(table, err)
		}
		if table == perStore {
			token = strings.Join(lines, " | ")
			continue
		}
		// Counted and hashed, so a failure names the table and shows no row.
		fmt.Fprintf(&out, "rows of %s: %d, sha256 %x\n", table, len(lines), sha256.Sum256([]byte(strings.Join(lines, "\x00"))))
	}
	return out.String(), token
}

func tableRows(db *sql.DB, table string) ([]string, error) {
	rows, err := db.Query(`SELECT * FROM "` + table + `" ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var lines []string
	for rows.Next() {
		values := make([]any, len(cols))
		pointers := make([]any, len(cols))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprint(values...))
	}
	return lines, rows.Err()
}
