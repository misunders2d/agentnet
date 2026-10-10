//go:build android || agentnet_sqlite_cgo

package sqlitedb

import (
	"database/sql"
	"database/sql/driver"
	"errors"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// The agentnet_sqlite_cgo tag is a focused host verifier for this adapter;
// release desktop builds keep modernc. Android uses SQLite through its supported C library/NDK rather than the
// desktop driver's translated Linux libc/syscalls. Keep the existing driver
// name and storage/schema surface; only established DSN spelling differs.
func init() { sql.Register("sqlite", androidSQLiteDriver{}) }

type androidSQLiteDriver struct{}

func (androidSQLiteDriver) Open(dsn string) (driver.Conn, error) {
	translated, err := androidSQLiteDSN(dsn)
	if err != nil {
		return nil, err
	}
	return (&sqlite3.SQLiteDriver{}).Open(translated)
}

// Busy recognizes actual SQLite primary lock errors, including wrapped errors.
func Busy(err error) bool {
	var e sqlite3.Error
	return errors.As(err, &e) && (e.Code == sqlite3.ErrBusy || e.Code == sqlite3.ErrLocked)
}
