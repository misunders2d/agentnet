package sqlitedb

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var v1 = []string{`CREATE TABLE a(x INTEGER);`}
var v2 = append(v1, `CREATE TABLE b(y INTEGER); INSERT INTO b(y) SELECT x FROM a;`)

func TestFailedCreateLeavesDatabaseReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	// Fails after the first statement already ran.
	if _, err := Open(path, []string{`CREATE TABLE a(x INTEGER); CREATE TABLE a(x INTEGER);`}); err == nil {
		t.Fatal("broken schema accepted")
	}
	db, err := Open(path, v2)
	if err != nil {
		t.Fatalf("reopen after failed create: %v", err)
	}
	db.Close()
}

func TestUpgradeKeepsDataAndIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO a(x) VALUES(7)`)
	db.Close()

	broken := append(v1, `CREATE TABLE c(z INTEGER); CREATE TABLE c(z INTEGER);`)
	if _, err := Open(path, broken); err == nil {
		t.Fatal("broken upgrade accepted")
	}
	db, err = Open(path, v2) // the failed step left version 1 intact
	if err != nil {
		t.Fatalf("upgrade after failed attempt: %v", err)
	}
	defer db.Close()
	var y int
	if err := db.QueryRow(`SELECT y FROM b`).Scan(&y); err != nil || y != 7 {
		t.Fatalf("upgraded data = %d, %v", y, err)
	}
}

func TestNewerSchemaRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if _, err := Open(path, v1); err == nil {
		t.Fatal("newer schema version accepted")
	}
}

func TestLooseExistingDatabaseRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits")
	}
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := os.Chmod(path, 0o644); err != nil { // e.g. restored from a backup
		t.Fatal(err)
	}
	if _, err := Open(path, v1); err == nil {
		t.Fatal("world-readable database accepted")
	}
}
