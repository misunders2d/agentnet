package client

import "database/sql"

// Membership checks read the same few statements for each person and device.
// Reuse their compilation within one check, while every execution still reads
// current rows through the original DB or transaction. This caches no authority
// or query results and owns no connection or statement beyond that call.
type membershipReads struct {
	dbq
	prepare func(string) (*sql.Stmt, error)
	stmts   map[string]*sql.Stmt
}

func prepareMembershipReads(q dbq) (dbq, func()) {
	if _, ok := q.(*membershipReads); ok {
		return q, func() {}
	}
	p, ok := q.(interface {
		Prepare(string) (*sql.Stmt, error)
	})
	if !ok {
		return q, func() {}
	}
	m := &membershipReads{dbq: q, prepare: p.Prepare, stmts: map[string]*sql.Stmt{}}
	return m, func() {
		for _, stmt := range m.stmts {
			stmt.Close()
		}
	}
}

func membershipReadSQL(query string) bool {
	switch query {
	case `SELECT ` + personCols + ` FROM persons WHERE person = ?`,
		`SELECT ` + personCols + ` FROM persons WHERE state = ?`,
		`SELECT ` + personCols + ` FROM persons WHERE person IN (SELECT person FROM person_devices WHERE address=?)`,
		`SELECT address, added FROM person_devices WHERE person = ?`,
		`SELECT hash FROM person_chain WHERE person = ?`,
		`SELECT count(*) FROM person_chain WHERE person = ? AND hash = ?`,
		`SELECT count(*) FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?`,
		`SELECT count(*) FROM group_withdrawals WHERE conv=? AND person=? AND admission=?`,
		`SELECT count(*) FROM (SELECT admission FROM group_withdrawals WHERE conv=? AND person=? AND admission=? UNION ALL SELECT admission FROM group_pending_withdrawals WHERE conv=? AND person=? AND admission=?)`:
		return true
	}
	return false
}

func (m *membershipReads) statement(query string) (*sql.Stmt, error) {
	if stmt := m.stmts[query]; stmt != nil {
		return stmt, nil
	}
	stmt, err := m.prepare(query)
	if err == nil {
		m.stmts[query] = stmt
	}
	return stmt, err
}

func (m *membershipReads) Query(query string, args ...any) (*sql.Rows, error) {
	if !membershipReadSQL(query) {
		return m.dbq.Query(query, args...)
	}
	stmt, err := m.statement(query)
	if err != nil {
		return nil, err
	}
	return stmt.Query(args...)
}

func (m *membershipReads) QueryRow(query string, args ...any) *sql.Row {
	if membershipReadSQL(query) {
		if stmt, err := m.statement(query); err == nil {
			return stmt.QueryRow(args...)
		}
	}
	// QueryRow reports errors on Scan; preserve that contract on prepare errors.
	return m.dbq.QueryRow(query, args...)
}
