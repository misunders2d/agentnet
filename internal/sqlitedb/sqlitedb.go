// Package sqlitedb opens SQLite databases with the settings every AgentNet store needs.
package sqlitedb

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"

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
		return fmt.Errorf("%s: schema version %d is newer than this program supports (%d)", path, current, len(steps))
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
	if _, err := os.Stat(snap); err == nil {
		return nil // an earlier attempt already saved it
	}
	if err := secfile.Touch(snap); err != nil {
		return err
	}
	if _, err := db.Exec(`VACUUM INTO ?`, snap); err != nil {
		os.Remove(snap)
		return fmt.Errorf("saving %s before upgrading: %w", snap, err)
	}
	return nil
}
