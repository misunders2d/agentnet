// Package sqlitedb opens SQLite databases with the settings every AgentNet store needs.
package sqlitedb

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Open opens path and brings its schema up to date. steps[i] is the SQL that
// moves a database from version i to i+1; all missing steps run in one
// transaction, so a failure leaves the previous version intact. Databases
// written by a newer version are refused. The database file must be
// owner-only: new files are created that way and existing files with wider
// access are refused. SQLite gives its WAL files the same mode.
func Open(path string, steps []string) (*sql.DB, error) {
	if err := secfile.Touch(path); err != nil {
		return nil, err
	}
	// Cooperating Open calls serialize the snapshot and migration together.
	// The existing OS lock releases on process death; waiting is synchronous.
	unlock, err := lockfile.Wait(path + ".upgrade.lock")
	if err != nil {
		return nil, err
	}
	defer unlock()
	remember, again, err := startFresh(path, steps)
	if err != nil {
		return nil, err
	}
	dsn, err := FileURI(path, "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection serialises writers and keeps SQLite free of lock errors.
	db.SetMaxOpenConns(1)
	if err := snapshotBeforeUpgrade(db, path, len(steps)); err != nil {
		db.Close()
		return nil, err
	}
	if err := upgrade(db, path, steps); err != nil {
		db.Close()
		return nil, err
	}
	if remember != nil {
		if err := remember(db, path); err != nil {
			db.Close()
			return nil, err
		}
	}
	if again != nil {
		if err := again(db); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// ReuseFreshSchema is for test fixtures whose subject is not the schema: with it on, a
// new empty store starts as a copy of the first one fully upgraded under exactly the same
// steps, instead of running every step again. again runs on each copy, for anything a
// step means to differ between stores. A store that already has anything in it upgrades
// as ever, and turning this off forgets every copy kept. It is off by default and no
// program turns it on; tests that prove schema steps, upgrades and backups leave it off.
// schematest proves a copy equals a stepped store.
func ReuseFreshSchema(on bool, again func(*sql.DB) error) {
	fresh.Lock()
	defer fresh.Unlock()
	fresh.on, fresh.again = on, again
	if !on {
		fresh.images = nil
	}
}

var fresh struct {
	sync.Mutex
	on     bool
	again  func(*sql.DB) error
	images map[[sha256.Size]byte][]byte
}

// startFresh gives an empty file the image for these steps. With no image yet, it
// returns what keeps one from this store once it is upgraded.
func startFresh(path string, steps []string) (remember func(*sql.DB, string) error, again func(*sql.DB) error, err error) {
	fresh.Lock()
	defer fresh.Unlock()
	if !fresh.on {
		return nil, nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Size() != 0 {
		return nil, nil, nil
	}
	key := sha256.Sum256([]byte(strings.Join(steps, "\x00")))
	if image, ok := fresh.images[key]; ok {
		return nil, fresh.again, os.WriteFile(path, image, 0o600)
	}
	return func(db *sql.DB, path string) error {
		copied := path + ".fresh"
		defer os.Remove(copied)
		if _, err := db.Exec(`VACUUM INTO ?`, copied); err != nil {
			return err
		}
		image, err := os.ReadFile(copied)
		if err != nil {
			return err
		}
		fresh.Lock()
		defer fresh.Unlock()
		if !fresh.on {
			return nil // turned off meanwhile: nothing may stay behind
		}
		if fresh.images == nil {
			fresh.images = map[[sha256.Size]byte][]byte{}
		}
		fresh.images[key] = image
		return nil
	}, nil, nil
}

// ErrNewerSchema means a newer program wrote the database: this one refuses
// to open it rather than guess.
var ErrNewerSchema = errors.New("is newer than this program supports")

func upgrade(db *sql.DB, path string, steps []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int
	if err := tx.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current > len(steps) {
		return fmt.Errorf("%s: schema version %d %w (%d)", path, current, ErrNewerSchema, len(steps))
	}
	if current == len(steps) {
		return nil
	}
	for i := current; i < len(steps); i++ {
		if _, err := tx.Exec(steps[i]); err != nil {
			return fmt.Errorf("%s: schema step %d: %w", path, i+1, err)
		}
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", len(steps))); err != nil {
		return err
	}
	return tx.Commit()
}

// snapshotBeforeUpgrade copies an existing database to PATH.vN.bak (N = its
// current version) before this program changes its schema, so an operator
// can go back to the previous release. The copy is owner-only.
func snapshotBeforeUpgrade(db *sql.DB, path string, target int) error {
	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current == 0 || current >= target {
		return nil
	}
	snap := fmt.Sprintf("%s.v%d.bak", path, current)
	if info, err := os.Lstat(snap); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: snapshot must be a regular owner-only file", snap)
		}
		if err := secfile.Touch(snap); err != nil {
			return err
		}
		if info.Size() != 0 {
			if err := validateSnapshot(snap, current); err != nil {
				return fmt.Errorf("%s: unusable pre-upgrade snapshot (preserved); restore a verified version-%d backup or move this file aside before retrying: %w", snap, current, err)
			}
			return secfile.SyncDir(filepath.Dir(snap))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// An interrupted INTO output never gets the published backup name. A stale
	// zero-byte target is replaced only after a complete snapshot is validated.
	tmp, err := secfile.CreateTemp(filepath.Dir(snap), filepath.Base(snap)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Close(); err != nil {
		return err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, name); err != nil {
		return fmt.Errorf("saving %s before upgrading: %w", snap, err)
	}
	if err := validateSnapshot(name, current); err != nil {
		return fmt.Errorf("validating %s before upgrading: %w", snap, err)
	}
	f, err := os.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(name, snap); err != nil {
		return err
	}
	return secfile.SyncDir(filepath.Dir(snap))
}

// FileURI keeps filename characters out of SQLite's URI options and fragment.
// Both writable stores and read-only snapshots must open the exact same path.
func FileURI(path, query string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: query}
	return u.String(), nil
}

func validateSnapshot(path string, version int) error {
	dsn, err := FileURI(path, "mode=ro&immutable=1")
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current != version {
		return fmt.Errorf("schema version %d, expected %d", current, version)
	}
	var integrity string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("SQLite integrity check failed")
	}
	return nil
}
