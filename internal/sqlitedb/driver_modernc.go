//go:build !android && !agentnet_sqlite_cgo

package sqlitedb

import (
	"errors"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Busy reports whether err is SQLite's busy or locked answer: another
// process held the database longer than the busy timeout. The same work may
// succeed when tried again.
func Busy(err error) bool {
	code := primaryCode(err)
	return code == sqlite3.SQLITE_BUSY || code == sqlite3.SQLITE_LOCKED
}

// Full reports whether SQLite refused a write for lack of storage capacity.
func Full(err error) bool { return primaryCode(err) == sqlite3.SQLITE_FULL }

func primaryCode(err error) int {
	var e *sqlite.Error
	if !errors.As(err, &e) {
		return 0
	}
	return e.Code() & 0xff // extended codes keep the primary code in the low byte
}
