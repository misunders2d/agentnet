package sqlitedb

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/misunders2d/agentnet/internal/secfile"
)

var v1 = []string{`CREATE TABLE a(x INTEGER);`}
var v2 = append(v1, `CREATE TABLE b(y INTEGER); INSERT INTO b(y) SELECT x FROM a;`)

func TestFreshSchemaIsOffByDefaultAndNoProgramTurnsItOn(t *testing.T) {
	if fresh.on || fresh.images != nil || fresh.again != nil {
		t.Fatal("the fresh image is on before anything asked for it")
	}
	db, err := Open(filepath.Join(t.TempDir(), "plain.db"), v1)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if fresh.images != nil {
		t.Fatal("a default Open kept an image")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", "static", "web":
				return filepath.SkipDir
			}
			if rel, _ := filepath.Rel(root, path); filepath.ToSlash(rel) == "internal/sqlitedb" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(text, []byte("ReuseFreshSchema")) || bytes.Contains(text, []byte("sqlitedb/schematest")) {
			t.Errorf("%s: program code reaches the test fixture's fresh image", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d program files", scanned)
	}
}

func freshCount(t *testing.T, db *sql.DB, query string) (n int) {
	t.Helper()
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFreshSchemaIsReusedOnlyForEmptyFiles(t *testing.T) {
	var again int
	ReuseFreshSchema(true, func(*sql.DB) error { again++; return nil })
	t.Cleanup(func() { ReuseFreshSchema(false, nil) })
	steps := []string{`CREATE TABLE fresh_a(x INTEGER); INSERT INTO fresh_a VALUES(7);`}
	dir := t.TempDir()
	// A step that fails is not remembered, and leaves the next store to run its own steps.
	broken := []string{`CREATE TABLE fresh_b(x INTEGER); CREATE TABLE fresh_b(x INTEGER);`}
	if _, err := Open(filepath.Join(dir, "broken.db"), broken); err == nil {
		t.Fatal("broken schema accepted")
	}
	if _, err := Open(filepath.Join(dir, "broken2.db"), broken); err == nil {
		t.Fatal("a broken schema came back as an image")
	}
	first, err := Open(filepath.Join(dir, "first.db"), steps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Exec(`INSERT INTO fresh_a VALUES(8)`); err != nil {
		t.Fatal(err)
	}
	first.Close()
	// Copies share nothing: not the first store's later rows, and not each other's.
	second, err := Open(filepath.Join(dir, "second.db"), steps)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if n := freshCount(t, second, `SELECT count(*) FROM fresh_a`); n != 1 {
		t.Fatalf("the image held %d rows, later writes included", n)
	}
	if again != 1 {
		t.Fatalf("again ran %d times, want once: only on a copy", again)
	}
	if _, err := second.Exec(`INSERT INTO fresh_a VALUES(9)`); err != nil {
		t.Fatal(err)
	}
	third, err := Open(filepath.Join(dir, "third.db"), steps)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if n := freshCount(t, third, `SELECT count(*) FROM fresh_a`); n != 1 {
		t.Fatalf("a copy saw another copy's write: %d rows", n)
	}
	// A store with anything in it keeps it, and upgrades through the steps it lacks.
	first, err = Open(filepath.Join(dir, "first.db"), append(steps[:1:1], `CREATE TABLE fresh_c(x INTEGER);`))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if n := freshCount(t, first, `SELECT count(*) FROM fresh_a`); n != 2 {
		t.Fatalf("existing store has %d rows after upgrading", n)
	}
	if n := freshCount(t, first, `SELECT count(*) FROM fresh_c`); n != 0 {
		t.Fatal("step not run")
	}
	if _, err := os.Stat(filepath.Join(dir, "first.db.v1.bak")); err != nil {
		t.Fatalf("an upgrade of a real store lost its backup: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "second.db"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("a store from the image has mode %v", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(dir, "first.db.fresh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a working copy stayed behind: %v", err)
	}
}

// No image outlives its steps or the switch: a schema change and turning it off and on
// again each end with what those exact steps make.
func TestFreshSchemaNeverServesAnotherSchemasImage(t *testing.T) {
	ReuseFreshSchema(true, nil)
	t.Cleanup(func() { ReuseFreshSchema(false, nil) })
	dir := t.TempDir()
	open := func(name string, steps []string) *sql.DB {
		t.Helper()
		db, err := Open(filepath.Join(dir, name), steps)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		return db
	}
	a := []string{`CREATE TABLE only_a(x INTEGER);`}
	b := []string{`CREATE TABLE only_b(x INTEGER);`}
	extended := append(a[:1:1], `CREATE TABLE only_c(x INTEGER);`)
	tables := func(db *sql.DB) (out string) {
		t.Helper()
		rows, err := db.Query(`SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			out += name + " "
		}
		return out
	}
	for i, c := range []struct {
		steps []string
		want  string
	}{{a, "only_a "}, {b, "only_b "}, {extended, "only_a only_c "}, {a, "only_a "}, {b, "only_b "}} {
		db := open(fmt.Sprintf("switch%d.db", i), c.steps)
		if got := tables(db); got != c.want {
			t.Fatalf("store %d has tables %q, want %q", i, got, c.want)
		}
		if v := freshCount(t, db, `PRAGMA user_version`); v != len(c.steps) {
			t.Fatalf("store %d is at version %d, want %d", i, v, len(c.steps))
		}
	}
	if len(fresh.images) != 3 {
		t.Fatalf("%d images for 3 schemas", len(fresh.images))
	}
	ReuseFreshSchema(false, nil)
	if fresh.images != nil {
		t.Fatal("turning it off kept the images")
	}
	var again int
	ReuseFreshSchema(true, func(*sql.DB) error { again++; return nil })
	if got := tables(open("again1.db", a)); got != "only_a " {
		t.Fatalf("after turning on again: %q", got)
	}
	if again != 0 {
		t.Fatal("turned on again, a store still came from an old image")
	}
	open("again2.db", a)
	if again != 1 {
		t.Fatalf("again ran %d times", again)
	}
}

func TestFreshSchemaConcurrentCreation(t *testing.T) {
	var mu sync.Mutex
	var copies int
	ReuseFreshSchema(true, func(db *sql.DB) error {
		mu.Lock()
		copies++
		mu.Unlock()
		_, err := db.Exec(`INSERT OR REPLACE INTO token VALUES(1,hex(randomblob(16)))`)
		return err
	})
	t.Cleanup(func() { ReuseFreshSchema(false, nil) })
	steps := []string{`CREATE TABLE token(id INTEGER PRIMARY KEY, value TEXT); INSERT INTO token VALUES(1,hex(randomblob(16)));`, `CREATE INDEX token_value ON token(value);`}
	dir := t.TempDir()
	const stores = 24
	tokens := make([]string, stores)
	var wg sync.WaitGroup
	errs := make(chan error, stores)
	for i := 0; i < stores; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each fourth goroutine opens a store another also opens: one creates it.
			name := fmt.Sprintf("s%d.db", i)
			if i%4 == 3 {
				name = fmt.Sprintf("s%d.db", i-1)
			}
			db, err := Open(filepath.Join(dir, name), steps)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			var version int
			var check string
			if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				errs <- err
				return
			}
			if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&check); err != nil {
				errs <- err
				return
			}
			if version != len(steps) || check != "ok" {
				errs <- fmt.Errorf("store %d: version %d, check %s", i, version, check)
				return
			}
			errs <- db.QueryRow(`SELECT value FROM token`).Scan(&tokens[i])
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	seen := map[string]int{}
	for i, token := range tokens {
		if i%4 == 3 {
			continue // the store of i-1
		}
		if at, again := seen[token]; again {
			t.Fatalf("stores %d and %d share their per-store row", at, i)
		}
		seen[token] = i
	}
	if len(seen) != stores-stores/4 {
		t.Fatalf("%d distinct stores", len(seen))
	}
}

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
