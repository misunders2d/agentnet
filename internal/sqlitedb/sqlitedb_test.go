package sqlitedb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

const good = `CREATE TABLE a(x INTEGER); CREATE TABLE b(y INTEGER);`

func TestFailedCreateLeavesDatabaseReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	if _, err := Open(path, `CREATE TABLE a(x INTEGER); CREATE TABLE a(x INTEGER);`, 1); err == nil { // fails after the first statement ran
		t.Fatal("broken schema accepted")
	}
	db, err := Open(path, good, 1)
	if err != nil {
		t.Fatalf("reopen after failed create: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO b(y) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, good, 2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path, good, 1); err == nil {
		t.Fatal("newer schema version accepted")
	}
}

func TestLooseExistingDatabaseRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, good, 1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(path, 0o644); err != nil { // e.g. restored from a backup
		t.Fatal(err)
	}
	if _, err := Open(path, good, 1); err == nil {
		t.Fatal("world-readable database accepted")
	}
}
