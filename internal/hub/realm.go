package hub

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// RealmID names this database's workspace, not its URL or TLS certificate.
// It grants no membership or trust; existing endpoint and key checks apply.
func (h *Hub) RealmID() string { return h.realmID }

// The migration leaves one explicitly uninitialized row. The first Open
// commits the cryptographically random identity and its initialized flag in
// one transaction. A crash before that commit can retry; a missing row or an
// invalid initialized identity is corruption, never a reason to replace it.
func (h *Hub) loadRealm() error {
	tx, err := h.store.db.Begin()
	if err != nil {
		return fmt.Errorf("load workspace identity: %w", err)
	}
	defer tx.Rollback()
	var id sql.NullString
	var initialized int
	if err := tx.QueryRow(`SELECT realm_id, initialized FROM realm WHERE id = 1`).Scan(&id, &initialized); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("corrupt Hub workspace identity: singleton row is missing")
		}
		return fmt.Errorf("load workspace identity: %w", err)
	}
	switch {
	case initialized == 0 && !id.Valid:
		id.String = protocol.NewID()
		if _, err := tx.Exec(`UPDATE realm SET realm_id = ?, initialized = 1 WHERE id = 1`, id.String); err != nil {
			return fmt.Errorf("record workspace identity: %w", err)
		}
	case initialized == 1 && id.Valid && validHex(id.String, 32):
		// Existing identity stays exactly as persisted.
	default:
		return errors.New("corrupt Hub workspace identity: expected a canonical 128-bit identifier")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("record workspace identity: %w", err)
	}
	h.realmID = id.String
	return nil
}
