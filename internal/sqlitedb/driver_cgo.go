//go:build android || agentnet_sqlite_cgo

package sqlitedb

import (
	"context"
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
	conn, err := (&sqlite3.SQLiteDriver{}).Open(translated)
	if err != nil {
		return nil, err
	}
	return &androidSQLiteConn{conn.(*sqlite3.SQLiteConn)}, nil
}

type androidSQLiteConn struct{ *sqlite3.SQLiteConn }

// The desktop driver uses deferred reads even with _txlock=immediate.
// mattn ignores ReadOnly, so retain its ordinary write behavior explicitly
// while allowing a WAL read snapshot to coexist with another writer.
func (c *androidSQLiteConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if !opts.ReadOnly {
		return c.SQLiteConn.BeginTx(ctx, opts)
	}
	if _, err := c.ExecContext(ctx, "BEGIN DEFERRED", nil); err != nil {
		return nil, err
	}
	return androidReadTx{c.SQLiteConn}, nil
}

type androidReadTx struct{ conn *sqlite3.SQLiteConn }

func (t androidReadTx) Commit() error {
	_, err := t.conn.Exec("COMMIT", nil)
	if err != nil {
		// Match SQLiteTx: database/sql ends the transaction on any commit
		// result, while SQLite may still need its connection rolled back.
		t.conn.Exec("ROLLBACK", nil)
	}
	return err
}

func (t androidReadTx) Rollback() error {
	_, err := t.conn.Exec("ROLLBACK", nil)
	return err
}

// Busy recognizes actual SQLite primary lock errors, including wrapped errors.
func Busy(err error) bool {
	code := primaryCode(err)
	return code == sqlite3.ErrBusy || code == sqlite3.ErrLocked
}

// Full reports whether SQLite refused a write for lack of storage capacity.
func Full(err error) bool { return primaryCode(err) == sqlite3.ErrFull }

func primaryCode(err error) sqlite3.ErrNo {
	var e sqlite3.Error
	if !errors.As(err, &e) {
		return 0
	}
	return e.Code
}
