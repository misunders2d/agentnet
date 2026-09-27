package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

const schemaVersion = 1

const schema = `
CREATE TABLE agents(
  address TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  public TEXT NOT NULL,
  admin INTEGER NOT NULL,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL);
CREATE TABLE invites(
  secret_hash TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  admin INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  created_by TEXT,
  used_by TEXT);
CREATE TABLE nonces(
  agent TEXT NOT NULL,
  nonce TEXT NOT NULL,
  seen_at INTEGER NOT NULL,
  PRIMARY KEY(agent, nonce));
CREATE TABLE messages(
  seq INTEGER PRIMARY KEY AUTOINCREMENT,
  id TEXT NOT NULL UNIQUE,
  sender TEXT NOT NULL,
  recipient TEXT NOT NULL,
  envelope BLOB NOT NULL,
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  delivered_at INTEGER);
CREATE INDEX messages_pending ON messages(recipient, state, seq);
`

var (
	errInviteInvalid = errors.New("invite invalid, expired, or already used")
	errAddressTaken  = errors.New("address already enrolled")
	errNotFound      = errors.New("not found")
	errIDConflict    = errors.New("message id already used for different content")
)

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	db, err := sqlitedb.Open(path, schema, schemaVersion)
	if err != nil {
		return nil, err
	}
	return &store{db}, nil
}

type agent struct {
	Public  identity.Public
	Admin   bool
	Revoked bool
}

func (s *store) agent(address string) (agent, error) {
	var a agent
	var pub string
	var revoked sql.NullInt64
	err := s.db.QueryRow(`SELECT public, admin, revoked_at FROM agents WHERE address = ?`, address).Scan(&pub, &a.Admin, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return a, errNotFound
	}
	if err != nil {
		return a, err
	}
	a.Revoked = revoked.Valid
	return a, json.Unmarshal([]byte(pub), &a.Public)
}

func (s *store) agentCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM agents`).Scan(&n)
	return n, err
}

func (s *store) createInvite(secret, label string, admin bool, ttl time.Duration, createdBy string) error {
	var by any
	if createdBy != "" {
		by = createdBy
	}
	_, err := s.db.Exec(`INSERT INTO invites(secret_hash, label, admin, expires_at, created_by) VALUES(?, ?, ?, ?, ?)`,
		protocol.HashSecret(secret), label, admin, time.Now().Add(ttl).Unix(), by)
	return err
}

// dropBootstrapInvites removes unused Hub-issued invites so a restart before
// the first join always leaves exactly one live bootstrap invite.
func (s *store) dropBootstrapInvites() error {
	_, err := s.db.Exec(`DELETE FROM invites WHERE created_by IS NULL AND used_by IS NULL`)
	return err
}

// enroll consumes the invite and registers the agent atomically. Replaying
// the exact same enrollment (same invite, address and keys) succeeds again so
// a client that lost the response can finish; any other reuse is refused.
// It reports whether the invite was the Hub-issued bootstrap invite.
func (s *store) enroll(secret string, pub identity.Public, label string) (bootstrap bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	data, err := json.Marshal(pub)
	if err != nil {
		return false, err
	}
	var invLabel string
	var admin bool
	var expires int64
	var createdBy, usedBy sql.NullString
	err = tx.QueryRow(`SELECT label, admin, expires_at, created_by, used_by FROM invites WHERE secret_hash = ?`,
		protocol.HashSecret(secret)).Scan(&invLabel, &admin, &expires, &createdBy, &usedBy)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && invLabel != label) {
		return false, errInviteInvalid
	}
	if err != nil {
		return false, err
	}
	if usedBy.Valid {
		var stored string
		var revoked sql.NullInt64
		err := tx.QueryRow(`SELECT public, revoked_at FROM agents WHERE address = ?`, usedBy.String).Scan(&stored, &revoked)
		if err == nil && usedBy.String == pub.Address && stored == string(data) && !revoked.Valid {
			return !createdBy.Valid, nil // exact replay
		}
		return false, errInviteInvalid
	}
	if expires <= time.Now().Unix() {
		return false, errInviteInvalid
	}
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM agents WHERE address = ?`, pub.Address).Scan(&exists); err != nil {
		return false, err
	}
	if exists > 0 {
		return false, errAddressTaken
	}
	if _, err := tx.Exec(`INSERT INTO agents(address, label, public, admin, created_at) VALUES(?, ?, ?, ?, ?)`,
		pub.Address, label, string(data), admin, time.Now().Unix()); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE invites SET used_by = ? WHERE secret_hash = ?`, pub.Address, protocol.HashSecret(secret)); err != nil {
		return false, err
	}
	return !createdBy.Valid, tx.Commit()
}

func (s *store) revoke(address string) error {
	res, err := s.db.Exec(`UPDATE agents SET revoked_at = ? WHERE address = ? AND revoked_at IS NULL`, time.Now().Unix(), address)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	return nil
}

// useNonce records a request nonce and reports false if it was already seen.
func (s *store) useNonce(agent, nonce string, now time.Time) (bool, error) {
	cutoff := now.Add(-2 * protocol.ClockSkew).Unix()
	if _, err := s.db.Exec(`DELETE FROM nonces WHERE seen_at < ?`, cutoff); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO nonces(agent, nonce, seen_at) VALUES(?, ?, ?)`, agent, nonce, now.Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// putMessage stores an envelope once. A retry with identical bytes returns
// the existing state; reuse of the id for other content is a conflict.
func (s *store) putMessage(id, sender, recipient string, envelope []byte) (string, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO messages(id, sender, recipient, envelope, state, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		id, sender, recipient, envelope, protocol.StateCustody, time.Now().Unix())
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return protocol.StateCustody, nil
	}
	var existing []byte
	var state string
	if err := s.db.QueryRow(`SELECT envelope, state FROM messages WHERE id = ? AND sender = ?`, id, sender).Scan(&existing, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errIDConflict
		}
		return "", err
	}
	if string(existing) != string(envelope) {
		return "", errIDConflict
	}
	return state, nil
}

type pending struct {
	Seq      int64
	Envelope []byte
}

func (s *store) pendingFor(recipient string, afterSeq int64) ([]pending, error) {
	rows, err := s.db.Query(`SELECT seq, envelope FROM messages
		WHERE recipient = ? AND state = ? AND seq > ? ORDER BY seq LIMIT 100`,
		recipient, protocol.StateCustody, afterSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.Seq, &p.Envelope); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// messageState returns the state of a message visible to its sender or recipient.
func (s *store) messageState(id, requester string) (sender, recipient, state string, err error) {
	err = s.db.QueryRow(`SELECT sender, recipient, state FROM messages WHERE id = ? AND (sender = ? OR recipient = ?)`,
		id, requester, requester).Scan(&sender, &recipient, &state)
	if errors.Is(err, sql.ErrNoRows) {
		err = errNotFound
	}
	return
}

// setDisposition records the recipient's receipt. custody may become
// delivered or quarantined, quarantined may become delivered, and delivered
// is final, so repeated or reordered acks cannot downgrade a message.
func (s *store) setDisposition(id, recipient, state string) (string, error) {
	res, err := s.db.Exec(`UPDATE messages SET state = ?, delivered_at = coalesce(delivered_at, ?)
		WHERE id = ? AND recipient = ? AND (state = ? OR (state = ? AND ? = ?))`,
		state, time.Now().Unix(), id, recipient,
		protocol.StateCustody, protocol.StateQuarantined, state, protocol.StateDelivered)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return state, nil
	}
	var current string
	err = s.db.QueryRow(`SELECT state FROM messages WHERE id = ? AND recipient = ?`, id, recipient).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return "", errNotFound
	}
	return current, err
}
