// Package sqlitedb opens SQLite databases with the settings every AgentNet store needs.
package sqlitedb

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

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
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
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
	return db, nil
}

// Busy reports whether err is SQLite's busy or locked answer: another
// process held the database longer than the busy timeout. The same work may
// succeed when tried again; any other database error says nothing of that.
func Busy(err error) bool {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return false
	}
	code := e.Code() & 0xff // extended codes keep the primary code in the low byte
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
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

func validateSnapshot(path string, version int) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&immutable=1"}
	db, err := sql.Open("sqlite", u.String())
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
