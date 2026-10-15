package client

import "database/sql"

// preparedReads reuses parsing within one bounded projection. Statements belong
// to the exact database/transaction supplied and are closed with that operation;
// this is not a cache of message data or authorization decisions.
type preparedReads struct {
	dbq
	statements map[string]*sql.Stmt
}

func (r *preparedReads) statement(query string) (*sql.Stmt, error) {
	if stmt := r.statements[query]; stmt != nil {
		return stmt, nil
	}
	p, ok := r.dbq.(interface {
		Prepare(string) (*sql.Stmt, error)
	})
	if !ok {
		return nil, nil
	}
	stmt, err := p.Prepare(query)
	if err == nil {
		r.statements[query] = stmt
	}
	return stmt, err
}

func (r *preparedReads) Query(query string, args ...any) (*sql.Rows, error) {
	stmt, err := r.statement(query)
	if err != nil {
		return nil, err
	}
	if stmt == nil {
		return r.dbq.Query(query, args...)
	}
	return stmt.Query(args...)
}

func (r *preparedReads) QueryRow(query string, args ...any) *sql.Row {
	stmt, err := r.statement(query)
	if err != nil || stmt == nil {
		return r.dbq.QueryRow(query, args...)
	}
	return stmt.QueryRow(args...)
}

func (r *preparedReads) close() {
	for _, stmt := range r.statements {
		stmt.Close()
	}
}

func prepareReads(q dbq) *preparedReads {
	return &preparedReads{dbq: q, statements: make(map[string]*sql.Stmt)}
}
