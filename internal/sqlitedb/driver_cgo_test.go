//go:build android || agentnet_sqlite_cgo

package sqlitedb

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	sqlite3 "github.com/mattn/go-sqlite3"
)

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
