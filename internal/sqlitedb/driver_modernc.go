//go:build !android && !agentnet_sqlite_cgo

package sqlitedb

import (
	"errors"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

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
