package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

const schemaVersion = 1

const schema = `
CREATE TABLE config(k TEXT PRIMARY KEY, v TEXT NOT NULL);
CREATE TABLE peers(
  address TEXT PRIMARY KEY,
  public TEXT NOT NULL,
  pending TEXT,
  pinned_at INTEGER NOT NULL);
CREATE TABLE outbox(
  id TEXT PRIMARY KEY,
  recipient TEXT NOT NULL,
  body TEXT NOT NULL,
  envelope TEXT NOT NULL,
  state TEXT NOT NULL,
  error TEXT,
  created_at INTEGER NOT NULL);
CREATE TABLE inbox(
  id TEXT PRIMARY KEY,
  sender TEXT NOT NULL,
  ts INTEGER NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL,
  reply_to TEXT,
  received_at INTEGER NOT NULL,
  read_at INTEGER,
  acked INTEGER NOT NULL DEFAULT 0);
CREATE TABLE quarantine(
  id TEXT PRIMARY KEY,
  sender TEXT NOT NULL,
  reason TEXT NOT NULL,
  envelope TEXT NOT NULL,
  received_at INTEGER NOT NULL,
  acked INTEGER NOT NULL DEFAULT 0);
`

// Outbox states. Hub states (custody, delivered) are stored as reported.
const (
	stateQueued = "queued" // not yet accepted by the Hub; retried
	stateFailed = "failed" // rejected by the Hub; not retried
)

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	db, err := sqlitedb.Open(path, schema, schemaVersion)
	if err != nil {
		return nil, err
	}
	return &store{db}, nil
}

func (s *store) setConfig(kv map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for k, v := range kv {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO config(k, v) VALUES(?, ?)`, k, v); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) deleteConfig(k string) error {
	_, err := s.db.Exec(`DELETE FROM config WHERE k = ?`, k)
	return err
}

func (s *store) config(k string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT v FROM config WHERE k = ?`, k).Scan(&v)
	return v, err
}

// peer returns the pinned key and any different key the directory now offers.
func (s *store) peer(address string) (pinned identity.Public, pending *identity.Public, found bool, err error) {
	var pub string
	var pend sql.NullString
	err = s.db.QueryRow(`SELECT public, pending FROM peers WHERE address = ?`, address).Scan(&pub, &pend)
	if errors.Is(err, sql.ErrNoRows) {
		return pinned, nil, false, nil
	}
	if err != nil {
		return
	}
	if err = json.Unmarshal([]byte(pub), &pinned); err != nil {
		return
	}
	if pend.Valid {
		pending = new(identity.Public)
		if err = json.Unmarshal([]byte(pend.String), pending); err != nil {
			return
		}
	}
	return pinned, pending, true, nil
}

func (s *store) pin(p identity.Public) error {
	data, _ := json.Marshal(p)
	_, err := s.db.Exec(`INSERT INTO peers(address, public, pinned_at) VALUES(?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET public = excluded.public, pending = NULL, pinned_at = excluded.pinned_at`,
		p.Address, string(data), time.Now().Unix())
	return err
}

func (s *store) setPending(p identity.Public) error {
	data, _ := json.Marshal(p)
	_, err := s.db.Exec(`UPDATE peers SET pending = ? WHERE address = ?`, string(data), p.Address)
	return err
}

func (s *store) addOutbox(env envelope.Envelope, body string) error {
	data, _ := json.Marshal(env)
	_, err := s.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		env.ID, env.To, body, string(data), stateQueued, time.Now().Unix())
	return err
}

func (s *store) setOutboxState(id, state, errText string) error {
	_, err := s.db.Exec(`UPDATE outbox SET state = ?, error = nullif(?, '') WHERE id = ?`, state, errText, id)
	return err
}

func (s *store) queued() ([]envelope.Envelope, error) {
	rows, err := s.db.Query(`SELECT envelope FROM outbox WHERE state = ? ORDER BY created_at`, stateQueued)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []envelope.Envelope
	for rows.Next() {
		var data string
		var env envelope.Envelope
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

// seen reports whether an envelope id is already in the inbox or quarantine.
func (s *store) seen(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE id = ?) + (SELECT count(*) FROM quarantine WHERE id = ?)`, id, id).Scan(&n)
	return n > 0, err
}

const insertInbox = `INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, reply_to, received_at) VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?)`

func inboxArgs(in envelope.Inner) []any {
	return []any{in.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, time.Now().Unix()}
}

func (s *store) addInbox(in envelope.Inner) error {
	_, err := s.db.Exec(insertInbox, inboxArgs(in)...)
	return err
}

// quarantine holds an envelope that failed verification, keyed by its id.
func (s *store) quarantine(id, sender, reason string, raw []byte) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO quarantine(id, sender, reason, envelope, received_at) VALUES(?, ?, ?, ?, ?)`,
		id, sender, reason, string(raw), time.Now().Unix())
	return err
}

// held returns envelopes quarantined for sender with reason, oldest first.
// Rows stay in place until each is promoted.
func (s *store) held(sender, reason string) ([]envelope.Envelope, error) {
	rows, err := s.db.Query(`SELECT envelope FROM quarantine WHERE sender = ? AND reason = ? ORDER BY received_at, id`, sender, reason)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []envelope.Envelope
	for rows.Next() {
		var data string
		var env envelope.Envelope
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			return nil, err
		}
		out = append(out, env)
	}
	return out, rows.Err()
}

// promote moves a verified message from quarantine to the inbox atomically;
// the new inbox row carries an unsent delivered receipt.
func (s *store) promote(in envelope.Inner) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(insertInbox, inboxArgs(in)...); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, in.ID); err != nil {
		return err
	}
	return tx.Commit()
}

type receipt struct{ id, state string }

// unsentReceipts lists dispositions not yet acknowledged to the Hub.
func (s *store) unsentReceipts() ([]receipt, error) {
	rows, err := s.db.Query(`SELECT id, ? FROM inbox WHERE acked = 0 UNION ALL SELECT id, ? FROM quarantine WHERE acked = 0`,
		protocol.StateDelivered, protocol.StateQuarantined)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []receipt
	for rows.Next() {
		var r receipt
		if err := rows.Scan(&r.id, &r.state); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// resendReceipt marks id's receipt unsent again; used when the Hub pushes a
// message we already processed, which means it never recorded our receipt.
func (s *store) resendReceipt(id string) error {
	if _, err := s.db.Exec(`UPDATE inbox SET acked = 0 WHERE id = ?`, id); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE quarantine SET acked = 0 WHERE id = ?`, id)
	return err
}

func (s *store) markAcked(r receipt) error {
	table := "inbox"
	if r.state == protocol.StateQuarantined {
		table = "quarantine"
	}
	_, err := s.db.Exec(`UPDATE `+table+` SET acked = 1 WHERE id = ?`, r.id)
	return err
}

// Message is a received message as shown to the user.
type Message struct {
	ID         string    `json:"id"`
	From       string    `json:"from"`
	Kind       string    `json:"kind"`
	Body       string    `json:"body"`
	ReplyTo    string    `json:"reply_to,omitempty"`
	SentAt     time.Time `json:"sent_at"`
	ReceivedAt time.Time `json:"received_at"`
	Read       bool      `json:"read"`
}

func (s *store) inbox(unreadOnly bool) ([]Message, error) {
	q := `SELECT id, sender, kind, body, coalesce(reply_to, ''), ts, received_at, read_at IS NOT NULL FROM inbox`
	if unreadOnly {
		q += ` WHERE read_at IS NULL`
	}
	rows, err := s.db.Query(q + ` ORDER BY received_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var ts, recv int64
		if err := rows.Scan(&m.ID, &m.From, &m.Kind, &m.Body, &m.ReplyTo, &ts, &recv, &m.Read); err != nil {
			return nil, err
		}
		m.SentAt, m.ReceivedAt = time.Unix(ts, 0), time.Unix(recv, 0)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *store) markRead(ids []string) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE inbox SET read_at = ? WHERE id = ? AND read_at IS NULL`, time.Now().Unix(), id); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) inboxSender(id string) (string, error) {
	var sender string
	err := s.db.QueryRow(`SELECT sender FROM inbox WHERE id = ?`, id).Scan(&sender)
	return sender, err
}
