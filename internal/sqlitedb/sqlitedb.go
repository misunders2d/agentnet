// Package sqlitedb opens SQLite databases with the settings every AgentNet store needs.
package sqlitedb

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/misunders2d/agentnet/internal/secfile"
)

// Open opens path, applies schema when the database is new, and refuses
// databases written by a newer schema version. The database file must be
// owner-only: new files are created that way and existing files with wider
// access are refused. SQLite gives its WAL files the same mode.
func Open(path, schema string, version int) (*sql.DB, error) {
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
	var current int
	if err := db.QueryRow("PRAGMA user_version").Scan(&current); err != nil {
		db.Close()
		return nil, err
	}
	switch {
	case current == version:
		return db, nil
	case current == 0:
		if err := create(db, schema, version); err != nil {
			db.Close()
			return nil, err
		}
		return db, nil
	default:
		db.Close()
		return nil, fmt.Errorf("%s: schema version %d not supported (want %d)", path, current, version)
	}
}

// create applies schema and records version in one transaction, so a failed
// first run leaves an empty database that the next Open initialises cleanly.
func create(db *sql.DB, schema string, version int) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}
