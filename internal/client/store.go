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

// schema lists the SQL steps from each version to the next.
var schema = []string{`
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
`, `
CREATE TABLE uploads(
  blob_id TEXT PRIMARY KEY,
  message_id TEXT NOT NULL,
  state TEXT NOT NULL);
CREATE TABLE attachments(
  message_id TEXT NOT NULL,
  blob_id TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  ct_size INTEGER NOT NULL,
  ct_sha256 TEXT NOT NULL,
  saved_path TEXT,
  PRIMARY KEY(message_id, blob_id));
`, `
ALTER TABLE inbox ADD COLUMN session TEXT;
ALTER TABLE outbox ADD COLUMN path TEXT;
CREATE TABLE routes(
  endpoint TEXT PRIMARY KEY,
  until INTEGER NOT NULL);
CREATE TABLE direct_blobs(
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  received INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,
  updated_at INTEGER NOT NULL);
`, `
ALTER TABLE inbox ADD COLUMN state TEXT NOT NULL DEFAULT '';
ALTER TABLE inbox ADD COLUMN status TEXT;
ALTER TABLE inbox ADD COLUMN responder TEXT;
ALTER TABLE inbox ADD COLUMN result_id TEXT;
ALTER TABLE inbox ADD COLUMN detail TEXT;
CREATE INDEX inbox_state ON inbox(state, received_at);
CREATE TABLE approvals(
  address TEXT PRIMARY KEY,
  added_at INTEGER NOT NULL);
`}

// Outbox states. Hub states (custody, delivered) are stored as reported.
const (
	stateQueued = "queued" // not yet accepted by the Hub; retried
	stateFailed = "failed" // rejected by the Hub; not retried
)

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	db, err := sqlitedb.Open(path, schema)
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

// addOutbox records a message and its pending uploads in one transaction,
// together with claim (if any), which marks what the message answers.
func (s *store) addOutbox(env envelope.Envelope, body string, claim func(tx *sql.Tx, replyID string) error) error {
	data, _ := json.Marshal(env)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if claim != nil {
		if err := claim(tx, env.ID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at) VALUES(?, ?, ?, ?, ?, ?)`,
		env.ID, env.To, body, string(data), stateQueued, time.Now().Unix()); err != nil {
		return err
	}
	for _, b := range env.Blobs {
		if _, err := tx.Exec(`INSERT INTO uploads(blob_id, message_id, state) VALUES(?, ?, ?)`, b.ID, env.ID, protocol.BlobUploading); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) uploadStored(blobID string) (bool, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM uploads WHERE blob_id = ?`, blobID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil // already released after custody
	}
	return state == protocol.BlobStored, err
}

func (s *store) setUploadStored(blobID string) error {
	_, err := s.db.Exec(`UPDATE uploads SET state = ? WHERE blob_id = ?`, protocol.BlobStored, blobID)
	return err
}

// releaseUploads forgets a message's uploads once the Hub holds the message.
func (s *store) releaseUploads(messageID string) error {
	_, err := s.db.Exec(`DELETE FROM uploads WHERE message_id = ?`, messageID)
	return err
}

func (s *store) setOutboxState(id, state, errText, path string) error {
	_, err := s.db.Exec(`UPDATE outbox SET state = ?, error = nullif(?, ''), path = coalesce(nullif(?, ''), path) WHERE id = ?`,
		state, errText, path, id)
	return err
}

// outboxState returns a sent message's last known state and path.
func (s *store) outboxState(id string) (state, path string, found bool, err error) {
	err = s.db.QueryRow(`SELECT state, coalesce(path, '') FROM outbox WHERE id = ?`, id).Scan(&state, &path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	return state, path, err == nil, err
}

// routeCooling reports whether a direct endpoint failed recently.
func (s *store) routeCooling(endpoint string, now time.Time) (bool, error) {
	var until int64
	err := s.db.QueryRow(`SELECT until FROM routes WHERE endpoint = ?`, endpoint).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && until > now.Unix(), err
}

func (s *store) coolRoute(endpoint string, until time.Time) error {
	_, err := s.db.Exec(`INSERT INTO routes(endpoint, until) VALUES(?, ?) ON CONFLICT(endpoint) DO UPDATE SET until = excluded.until`,
		endpoint, until.Unix())
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

const insertInbox = `INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, reply_to, received_at, session, status, state)
	VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?)`

// Response states of received questions and tasks. They are independent of
// read/unread: reading never makes anything run.
const (
	statePending   = "pending"  // eligible for the worker
	stateHeld      = "held"     // question from a sender not approved for automatic answers
	stateAwaiting  = "awaiting" // task waiting for the local human to accept
	stateRunning   = "running"  // the worker owns it
	stateCancelReq = "cancel_requested"
	stateAnswered  = "answered"  // the worker replied
	stateManual    = "manual"    // someone replied by hand
	stateDeclined  = "declined"  // the local human declined the task
	stateJobFailed = "failed"    // the worker ran and failed or timed out
	stateCancelled = "cancelled" // cancelled while running
	stateInterrupt = "interrupted"
)

func inboxArgs(in envelope.Inner, state string) []any {
	return []any{in.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, time.Now().Unix(), in.Session, in.Status, state}
}

// initialState decides whether a new message waits for anything.
func initialState(db querier, in envelope.Inner) (string, error) {
	switch in.Kind {
	case envelope.KindQuestion:
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM approvals WHERE address = ?`, in.From).Scan(&n); err != nil {
			return "", err
		}
		if n > 0 {
			return statePending, nil
		}
		return stateHeld, nil
	case envelope.KindTask:
		return stateAwaiting, nil
	}
	return "", nil
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// insertInner stores a verified message and its attachment manifest.
func insertInner(tx *sql.Tx, in envelope.Inner) error {
	state, err := initialState(tx, in)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(insertInbox, inboxArgs(in, state)...); err != nil {
		return err
	}
	db := tx
	for _, a := range in.Attachments {
		if _, err := db.Exec(`INSERT OR IGNORE INTO attachments(message_id, blob_id, name, size, sha256, ct_size, ct_sha256) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			in.ID, a.Blob.ID, a.Name, a.Size, a.SHA256, a.Blob.Size, a.Blob.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) addInbox(in envelope.Inner) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertInner(tx, in); err != nil {
		return err
	}
	return tx.Commit()
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
	if err := insertInner(tx, in); err != nil {
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
	ID          string     `json:"id"`
	From        string     `json:"from"`
	Kind        string     `json:"kind"`
	State       string     `json:"state,omitempty"`  // response state of a question or task
	Status      string     `json:"status,omitempty"` // outcome carried by an answer or result
	Responder   string     `json:"responder,omitempty"`
	Detail      string     `json:"detail,omitempty"`
	Body        string     `json:"body"`
	ReplyTo     string     `json:"reply_to,omitempty"`
	SentAt      time.Time  `json:"sent_at"`
	ReceivedAt  time.Time  `json:"received_at"`
	Read        bool       `json:"read"`
	Attachments []FileInfo `json:"attachments,omitempty"`
}

// FileInfo describes a received attachment from its encrypted manifest.
type FileInfo struct {
	BlobID    string `json:"blob_id"`
	Name      string `json:"name"` // as the sender named it; not a safe path
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	SavedPath string `json:"saved_path,omitempty"`

	ctSize   int64
	ctSHA256 string
}

func (s *store) inbox(unreadOnly bool) ([]Message, error) {
	q := `SELECT id, sender, kind, body, coalesce(reply_to, ''), ts, received_at, read_at IS NOT NULL,
		state, coalesce(status, ''), coalesce(responder, ''), coalesce(detail, '') FROM inbox`
	if unreadOnly {
		q += ` WHERE read_at IS NULL`
	}
	rows, err := s.db.Query(q + ` ORDER BY received_at, id`)
	if err != nil {
		return nil, err
	}
	var out []Message
	for rows.Next() {
		var m Message
		var ts, recv int64
		if err := rows.Scan(&m.ID, &m.From, &m.Kind, &m.Body, &m.ReplyTo, &ts, &recv, &m.Read,
			&m.State, &m.Status, &m.Responder, &m.Detail); err != nil {
			rows.Close()
			return nil, err
		}
		m.SentAt, m.ReceivedAt = time.Unix(ts, 0), time.Unix(recv, 0)
		out = append(out, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Attachments, err = s.attachments(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *store) attachments(messageID string) ([]FileInfo, error) {
	rows, err := s.db.Query(`SELECT blob_id, name, size, sha256, ct_size, ct_sha256, coalesce(saved_path, '')
		FROM attachments WHERE message_id = ? ORDER BY rowid`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileInfo
	for rows.Next() {
		var f FileInfo
		if err := rows.Scan(&f.BlobID, &f.Name, &f.Size, &f.SHA256, &f.ctSize, &f.ctSHA256, &f.SavedPath); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *store) setSaved(messageID, blobID, path string) error {
	_, err := s.db.Exec(`UPDATE attachments SET saved_path = ? WHERE message_id = ? AND blob_id = ?`, path, messageID, blobID)
	return err
}

func (s *store) markRead(ids []string) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE inbox SET read_at = ? WHERE id = ? AND read_at IS NULL`, time.Now().Unix(), id); err != nil {
			return err
		}
	}
	return nil
}

func (s *store) inboxKind(id string) (sender, kind string, err error) {
	err = s.db.QueryRow(`SELECT sender, kind FROM inbox WHERE id = ?`, id).Scan(&sender, &kind)
	return
}

// disposition reports how a received message was filed.
func (s *store) disposition(id string) (string, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM inbox WHERE id = ?`, id).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return protocol.StateDelivered, nil
	}
	if err := s.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ?`, id).Scan(&n); err != nil {
		return "", err
	}
	if n > 0 {
		return protocol.StateQuarantined, nil
	}
	return "", errors.New("message not stored")
}

// job is a received question or task the worker has claimed.
type job struct {
	ID, From, Kind, Body, ReplyTo string
	Attachments                   int
}

// claimJob gives the oldest pending question or task to the worker,
// recording which responder took it. Only one caller can win a row.
func (s *store) claimJob(responder string) (job, bool, error) {
	var j job
	err := s.db.QueryRow(`UPDATE inbox SET state = ?, responder = ?, detail = NULL
		WHERE id = (SELECT id FROM inbox WHERE state = ? ORDER BY received_at, id LIMIT 1) AND state = ?
		RETURNING id, sender, kind, body, coalesce(reply_to, '')`,
		stateRunning, responder, statePending, statePending).Scan(&j.ID, &j.From, &j.Kind, &j.Body, &j.ReplyTo)
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	if err != nil {
		return j, false, err
	}
	err = s.db.QueryRow(`SELECT count(*) FROM attachments WHERE message_id = ?`, j.ID).Scan(&j.Attachments)
	return j, true, err
}

func (s *store) jobState(id string) (string, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM inbox WHERE id = ?`, id).Scan(&state)
	return state, err
}

// finishJob records a job's end without a reply.
func (s *store) finishJob(id, state, detail string) error {
	_, err := s.db.Exec(`UPDATE inbox SET state = ?, detail = nullif(?, '') WHERE id = ? AND state IN (?, ?)`,
		state, detail, id, stateRunning, stateCancelReq)
	return err
}

// interruptRunning marks jobs a previous daemon left running. They are not
// rerun automatically: a task may already have had effects.
func (s *store) interruptRunning() error {
	_, err := s.db.Exec(`UPDATE inbox SET state = ?, detail = 'the daemon stopped while this was running'
		WHERE state IN (?, ?)`, stateInterrupt, stateRunning, stateCancelReq)
	return err
}

// threadText returns up to max earlier messages of the conversation ending
// at id (oldest first), following reply_to through inbox and outbox.
func (s *store) threadText(replyTo string, max int) ([]string, error) {
	var out []string
	for id := replyTo; id != "" && len(out) < max; {
		var who, body, next string
		err := s.db.QueryRow(`SELECT sender, body, coalesce(reply_to, '') FROM inbox WHERE id = ?`, id).Scan(&who, &body, &next)
		if errors.Is(err, sql.ErrNoRows) {
			var env string
			err = s.db.QueryRow(`SELECT body, envelope FROM outbox WHERE id = ?`, id).Scan(&body, &env)
			if errors.Is(err, sql.ErrNoRows) {
				break
			}
			who, next = "me", ""
		}
		if err != nil {
			return nil, err
		}
		out = append([]string{who + ": " + body}, out...)
		id = next
	}
	return out, nil
}

func (s *store) outboxEnvelope(id string) (envelope.Envelope, error) {
	var data string
	var env envelope.Envelope
	if err := s.db.QueryRow(`SELECT envelope FROM outbox WHERE id = ?`, id).Scan(&data); err != nil {
		return env, err
	}
	return env, json.Unmarshal([]byte(data), &env)
}
