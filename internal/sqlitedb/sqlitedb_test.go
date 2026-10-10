package sqlitedb

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
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

func TestSnapshotBeforeUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	db.Exec(`INSERT INTO a(x) VALUES(7)`)
	db.Close()
	if db, err = Open(path, v2); err != nil {
		t.Fatal(err)
	}
	db.Close()
	old, err := Open(path+".v1.bak", v1) // the previous release can open it
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	defer old.Close()
	var x int
	if err := old.QueryRow(`SELECT x FROM a`).Scan(&x); err != nil || x != 7 {
		t.Fatalf("snapshot data %d, %v", x, err)
	}
}

func TestEmptySnapshotRebuiltBeforeUpgrade(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO a VALUES(7)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	backup := path + ".v1.bak"
	if err = secfile.Write(backup, nil); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path, v2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	info, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("upgrade accepted empty pre-upgrade snapshot")
	}
	old, err := Open(backup, v1)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var x int
	if err = old.QueryRow("SELECT x FROM a").Scan(&x); err != nil || x != 7 {
		t.Fatalf("snapshot data %d %v", x, err)
	}
}

func TestExistingSnapshotPreserved(t *testing.T) {
	for _, kind := range []string{"valid", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "t.db")
			db, err := Open(path, v1)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("INSERT INTO a VALUES(7)"); err != nil {
				t.Fatal(err)
			}
			snap := path + ".v1.bak"
			if kind == "valid" {
				if err = secfile.Touch(snap); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec("VACUUM INTO ?", snap); err != nil {
					t.Fatal(err)
				}
			} else {
				if err = secfile.Write(snap, []byte("nonempty interrupted snapshot")); err != nil {
					t.Fatal(err)
				}
			}
			db.Close()
			before, err := os.ReadFile(snap)
			if err != nil {
				t.Fatal(err)
			}
			upgraded, err := Open(path, v2)
			if kind == "corrupt" && err == nil {
				upgraded.Close()
				t.Fatal("corrupt backup allowed migration")
			}
			if kind == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				upgraded.Close()
			}
			after, err := os.ReadFile(snap)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("existing backup changed: %v", err)
			}
			if kind == "corrupt" {
				original, err := Open(path, v1)
				if err != nil {
					t.Fatal(err)
				}
				defer original.Close()
				var x int
				if err = original.QueryRow("SELECT x FROM a").Scan(&x); err != nil || x != 7 {
					t.Fatalf("source changed: %d %v", x, err)
				}
				var n int
				if err = original.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='b'").Scan(&n); err != nil || n != 0 {
					t.Fatalf("migration ran: %d %v", n, err)
				}
			}
		})
	}
}

func TestInterruptedSnapshotTempDoesNotBlockRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO a VALUES(7)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	stale := path + ".v1.bak.tmp-interrupted"
	if err = os.WriteFile(stale, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path, v2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	old, err := Open(path+".v1.bak", v1)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var x int
	if err = old.QueryRow("SELECT x FROM a").Scan(&x); err != nil || x != 7 {
		t.Fatalf("retry snapshot: %d %v", x, err)
	}
	data, err := os.ReadFile(stale)
	if err != nil || string(data) != "partial" {
		t.Fatalf("unrelated stale temp removed: %v", err)
	}
}

func TestSnapshotFailureAndMigrationRollbackPreserveSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO a VALUES(7)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	broken := append(v1, `CREATE TABLE c(z); INSERT INTO a VALUES(99); CREATE TABLE c(z);`)
	if _, err = Open(path, broken); err == nil {
		t.Fatal("broken migration accepted")
	}
	before, err := os.ReadFile(path + ".v1.bak")
	if err != nil {
		t.Fatal(err)
	}
	db, err = Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	var version, count, x, n int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("version %d %v", version, err)
	}
	if err = db.QueryRow("SELECT count(*),max(x) FROM a").Scan(&count, &x); err != nil || count != 1 || x != 7 {
		t.Fatalf("source data %d %d %v", count, x, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='c'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partial schema %d %v", n, err)
	}
	db.Close()
	db, err = Open(path, v2)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	after, err := os.ReadFile(path + ".v1.bak")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("retry changed valid snapshot %v", err)
	}
}

func TestConcurrentUpgradeKeepsOriginalSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO a VALUES(7)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err = secfile.Write(path+".v1.bak", nil); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			upgraded, e := Open(path, v2)
			if e == nil {
				e = upgraded.Close()
			}
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	old, err := Open(path+".v1.bak", v1)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var version, x int
	if err = old.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatalf("snapshot version %d %v", version, err)
	}
	if err = old.QueryRow("SELECT x FROM a").Scan(&x); err != nil || x != 7 {
		t.Fatalf("snapshot data %d %v", x, err)
	}
}

func TestSnapshotReadOnlyValidationRejectsWrongVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	backup, err := Open(path+".v1.bak", v2)
	if err != nil {
		t.Fatal(err)
	}
	backup.Close()
	before, err := os.ReadFile(path + ".v1.bak")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Open(path, v2); err == nil {
		t.Fatal("wrong-version backup accepted")
	}
	after, err := os.ReadFile(path + ".v1.bak")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("wrong-version backup changed %v", err)
	}
}

// Busy tells another process's lock (worth trying again) from any other
// database error.
func TestBusyOnlyForLocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.db")
	db, err := Open(path, v1)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := db.Begin() // immediate: holds the write lock
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO a(x) VALUES(1)`); err != nil {
		t.Fatal(err)
	}
	_, err = other.Exec(`INSERT INTO a(x) VALUES(2)`)
	if err == nil || !Busy(err) || !Busy(fmt.Errorf("wrapped: %w", err)) {
		t.Fatalf("a locked database is not busy: %v", err)
	}
	if _, err = other.Exec(`INSERT INTO missing(x) VALUES(1)`); err == nil || Busy(err) {
		t.Fatalf("a missing table is busy: %v", err)
	}
	if Busy(errors.New("database is locked")) || Busy(nil) {
		t.Fatal("text alone is busy")
	}
}
