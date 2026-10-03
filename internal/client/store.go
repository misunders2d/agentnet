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
`, `
ALTER TABLE outbox ADD COLUMN reply_to TEXT;
`, `
ALTER TABLE outbox ADD COLUMN follow_up TEXT;
ALTER TABLE outbox ADD COLUMN followed_by TEXT;
ALTER TABLE inbox ADD COLUMN notified INTEGER NOT NULL DEFAULT 0;
`, `
ALTER TABLE inbox ADD COLUMN arrival INTEGER;
UPDATE inbox SET arrival = rowid;
CREATE UNIQUE INDEX inbox_arrival ON inbox(arrival);
INSERT INTO config(k, v) SELECT 'arrival', coalesce(max(arrival), 0) FROM inbox;
ALTER TABLE outbox ADD COLUMN status TEXT;
CREATE TABLE sent_attachments(
  message_id TEXT NOT NULL,
  blob_id TEXT NOT NULL,
  name TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  PRIMARY KEY(message_id, blob_id));
CREATE TABLE attention(
  harness TEXT NOT NULL,
  session TEXT NOT NULL,
  pos INTEGER NOT NULL,
  seen_at INTEGER NOT NULL,
  PRIMARY KEY(harness, session));
`, `
CREATE TRIGGER inbox_arrival AFTER INSERT ON inbox WHEN NEW.arrival IS NULL
BEGIN
  UPDATE config SET v = CAST(v AS INTEGER) + 1 WHERE k = 'arrival';
  UPDATE inbox SET arrival = (SELECT CAST(v AS INTEGER) FROM config WHERE k = 'arrival') WHERE rowid = NEW.rowid;
END;
UPDATE inbox SET arrival = (SELECT CAST(v AS INTEGER) FROM config WHERE k = 'arrival') + r.n
  FROM (SELECT rowid AS rid, row_number() OVER (ORDER BY rowid) AS n FROM inbox WHERE arrival IS NULL) AS r
  WHERE inbox.rowid = r.rid;
UPDATE config SET v = (SELECT max(arrival) FROM inbox)
  WHERE k = 'arrival' AND (SELECT max(arrival) FROM inbox) > CAST(v AS INTEGER);
CREATE INDEX inbox_links ON inbox(sender, id, reply_to, received_at);
CREATE INDEX outbox_links ON outbox(recipient, id, reply_to, created_at);
`, `
ALTER TABLE inbox ADD COLUMN session_ref TEXT;
`, `
ALTER TABLE attention ADD COLUMN release_seen TEXT;
`, `
ALTER TABLE inbox ADD COLUMN review_sent INTEGER NOT NULL DEFAULT 0;
`, `
ALTER TABLE inbox ADD COLUMN verified_by TEXT;
CREATE TABLE task_grants(
  address TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL,
  public TEXT NOT NULL,
  added_at INTEGER NOT NULL);
`, `
CREATE TABLE persons(
  person TEXT PRIMARY KEY,
  address TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  hash TEXT NOT NULL,
  roster TEXT NOT NULL,
  label TEXT NOT NULL,
  state TEXT NOT NULL,
  conflict TEXT,
  pinned_at INTEGER NOT NULL);
CREATE UNIQUE INDEX persons_address ON persons(address);
CREATE TABLE conversations(
  id TEXT PRIMARY KEY,
  root TEXT NOT NULL,
  kind TEXT NOT NULL,
  peer TEXT NOT NULL,
  pinned_at INTEGER NOT NULL);
ALTER TABLE inbox ADD COLUMN conv TEXT;
ALTER TABLE inbox ADD COLUMN lid TEXT;
ALTER TABLE inbox ADD COLUMN sub TEXT;
ALTER TABLE inbox ADD COLUMN replica INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox ADD COLUMN origin TEXT;
ALTER TABLE inbox ADD COLUMN emotion TEXT;
ALTER TABLE inbox ADD COLUMN target TEXT;
ALTER TABLE inbox ADD COLUMN content_hash TEXT;
ALTER TABLE inbox ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE inbox ADD COLUMN last_attempt_at INTEGER;
ALTER TABLE inbox ADD COLUMN received_ms INTEGER;
CREATE UNIQUE INDEX inbox_logical ON inbox(verified_by, lid) WHERE lid IS NOT NULL;
CREATE INDEX inbox_conv ON inbox(conv, received_at) WHERE conv IS NOT NULL;
ALTER TABLE outbox ADD COLUMN conv TEXT;
ALTER TABLE outbox ADD COLUMN lid TEXT;
ALTER TABLE outbox ADD COLUMN kind TEXT;
ALTER TABLE outbox ADD COLUMN origin TEXT;
ALTER TABLE outbox ADD COLUMN emotion TEXT;
ALTER TABLE outbox ADD COLUMN target TEXT;
ALTER TABLE outbox ADD COLUMN created_ms INTEGER;
CREATE INDEX outbox_conv ON outbox(conv, created_at) WHERE conv IS NOT NULL;
`, `
ALTER TABLE persons ADD COLUMN public TEXT;
DROP INDEX inbox_links;
CREATE INDEX inbox_links ON inbox(sender, id, reply_to, received_at, conv);
DROP INDEX outbox_links;
CREATE INDEX outbox_links ON outbox(recipient, id, reply_to, created_at, conv);
`, `
CREATE TABLE participation_events(
  hash TEXT PRIMARY KEY,
  conv TEXT NOT NULL,
  pid TEXT NOT NULL,
  type TEXT NOT NULL,
  author TEXT NOT NULL,
  event TEXT NOT NULL,
  received_at INTEGER NOT NULL);
CREATE INDEX participation_events_pid ON participation_events(conv, pid);
CREATE INDEX participation_events_type ON participation_events(conv, type, pid);
ALTER TABLE inbox ADD COLUMN pid TEXT;
ALTER TABLE outbox ADD COLUMN pid TEXT;
ALTER TABLE outbox ADD COLUMN sub TEXT;
`, `
ALTER TABLE participation_events ADD COLUMN prev TEXT;
UPDATE participation_events SET prev = json_extract(event, '$.prev');
CREATE INDEX participation_events_prev ON participation_events(conv, pid, type, author);
`, `
ALTER TABLE inbox ADD COLUMN local INTEGER NOT NULL DEFAULT 0;
CREATE INDEX inbox_agent_jobs ON inbox(arrival) WHERE pid IS NOT NULL AND state IN ('part_waiting', 'accepted');
`, `
CREATE TABLE alert_senders(
  address TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL);
CREATE TABLE alert_mutes(
  conv TEXT PRIMARY KEY);
CREATE TABLE alerts(
  conv TEXT PRIMARY KEY,
  sender TEXT NOT NULL,
  last_id TEXT NOT NULL,
  count INTEGER NOT NULL,
  due_ms INTEGER NOT NULL);
CREATE INDEX alerts_due ON alerts(due_ms);
`, `
CREATE TABLE reminders(
  message TEXT PRIMARY KEY,
  conv TEXT,
  due_at INTEGER NOT NULL,
  state TEXT NOT NULL,
  rev INTEGER NOT NULL,
  notified_rev INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);
CREATE INDEX reminders_due ON reminders(due_at) WHERE state = 'pending';
`, `
ALTER TABLE persons RENAME TO persons_v1;
ALTER TABLE conversations RENAME TO conversations_v1;
CREATE TABLE persons(
  person TEXT PRIMARY KEY,
  label TEXT NOT NULL,
  seq INTEGER NOT NULL,
  hash TEXT NOT NULL,
  record TEXT NOT NULL,
  state TEXT NOT NULL,
  conflict TEXT,
  pinned_at INTEGER NOT NULL);
CREATE TABLE person_chain(
  person TEXT NOT NULL,
  seq INTEGER NOT NULL,
  hash TEXT NOT NULL,
  record TEXT NOT NULL,
  PRIMARY KEY(person, seq));
CREATE INDEX person_chain_hash ON person_chain(person, hash);
CREATE TABLE person_devices(
  address TEXT PRIMARY KEY,
  person TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  added INTEGER NOT NULL);
CREATE INDEX person_devices_person ON person_devices(person);
CREATE TABLE conversations(
  id TEXT PRIMARY KEY,
  root TEXT NOT NULL,
  kind TEXT NOT NULL,
  peer TEXT NOT NULL,
  pinned_at INTEGER NOT NULL);
CREATE TABLE link_offers(
  offer TEXT PRIMARY KEY,
  secret BLOB NOT NULL,
  person TEXT NOT NULL,
  seq INTEGER NOT NULL,
  roster TEXT NOT NULL,
  expires INTEGER NOT NULL,
  used INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL);
CREATE TABLE device_links(
  offer TEXT PRIMARY KEY,
  address TEXT NOT NULL,
  public TEXT NOT NULL,
  join_sig BLOB NOT NULL,
  requested_at INTEGER NOT NULL,
  expires INTEGER NOT NULL,
  state TEXT NOT NULL,
  roster TEXT,
  detail TEXT,
  updated_at INTEGER NOT NULL);
`, `
ALTER TABLE inbox ADD COLUMN claimed_fp TEXT;
ALTER TABLE inbox ADD COLUMN via TEXT;
CREATE UNIQUE INDEX inbox_logical_any ON inbox(coalesce(verified_by, claimed_fp), lid) WHERE lid IS NOT NULL;
CREATE TABLE history_jobs(
  device TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL,
  pos TEXT NOT NULL,
  convs_total INTEGER NOT NULL,
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);
`, `
CREATE TABLE file_requests(
  message_id TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  state TEXT NOT NULL,
  detail TEXT,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY(message_id, sha256));
CREATE TABLE file_serves(
  id TEXT PRIMARY KEY,
  device TEXT NOT NULL,
  conv TEXT NOT NULL,
  lid TEXT NOT NULL,
  sha256 TEXT NOT NULL,
  state TEXT NOT NULL,
  updated_at INTEGER NOT NULL);
`, `
ALTER TABLE inbox ADD COLUMN ref_id TEXT;
ALTER TABLE inbox ADD COLUMN ref_fp TEXT;
ALTER TABLE outbox ADD COLUMN ref_id TEXT;
ALTER TABLE outbox ADD COLUMN ref_fp TEXT;
CREATE INDEX inbox_ref ON inbox(ref_id, ref_fp) WHERE ref_id IS NOT NULL;
CREATE INDEX outbox_ref ON outbox(ref_id, ref_fp) WHERE ref_id IS NOT NULL;
`, `
CREATE TABLE operators(
  address TEXT PRIMARY KEY,
  fingerprint TEXT NOT NULL,
  public TEXT NOT NULL,
  added_at INTEGER NOT NULL);
CREATE TABLE reported(
  item TEXT NOT NULL,
  recipient TEXT NOT NULL,
  sent_at INTEGER NOT NULL,
  PRIMARY KEY(item, recipient));
`, TeamSchema, GroupClientSchema, GroupProofSchema, agentIdentitySchema, agentCapabilitySchema, groupTurnRecipientSchema, replyReceiverSchema, GroupLifecycleSchema, replySessionSchema, GroupHistorySchema, receiverRouteSchema, humanScopeSchema, convClearSchema}

// Outbox states. Hub states (custody, delivered) are stored as reported.
const (
	stateQueued = "queued" // not yet accepted by the Hub; retried
	stateFailed = "failed" // rejected by the Hub; not retried
)

type store struct {
	db       *sql.DB
	onChange func() // set by the Agent: local state changed (changes.go)
}

func openStore(path string) (*store, error) {
	db, err := sqlitedb.Open(path, schema)
	if err != nil {
		return nil, err
	}
	return &store{db: db}, nil
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

// pin trusts p for its address. If that replaces a different key, tasks
// that were to run under a task grant go back to waiting for the person.
func (s *store) pin(p identity.Public) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	old, found, err := pinnedKey(tx, p.Address)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(p)
	if _, err := tx.Exec(`INSERT INTO peers(address, public, pinned_at) VALUES(?, ?, ?)
		ON CONFLICT(address) DO UPDATE SET public = excluded.public, pending = NULL, pinned_at = excluded.pinned_at`,
		p.Address, string(data), time.Now().Unix()); err != nil {
		return err
	}
	if found && old.Fingerprint() != p.Fingerprint() {
		if err := demoteGranted(tx, p.Address, "", "the sender's key changed"); err != nil {
			return err
		}
	}
	return s.done(tx.Commit())
}

// setPending records a different key the directory offers for p's
// address. Until it is resolved, no task from there runs without asking.
func (s *store) setPending(p identity.Public) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	data, _ := json.Marshal(p)
	if _, err := tx.Exec(`UPDATE peers SET pending = ? WHERE address = ?`, string(data), p.Address); err != nil {
		return err
	}
	if err := demoteGranted(tx, p.Address, "", "the sender's key may have changed"); err != nil {
		return err
	}
	return s.done(tx.Commit())
}

// pinnedKey returns the key pinned for address, read within q.
func pinnedKey(q querier, address string) (identity.Public, bool, error) {
	var pub identity.Public
	var data string
	err := q.QueryRow(`SELECT public FROM peers WHERE address = ?`, address).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return pub, false, nil
	}
	if err != nil {
		return pub, false, err
	}
	return pub, true, json.Unmarshal([]byte(data), &pub)
}

// addOutbox records a message, its attachment manifest and its pending
// uploads in one transaction, together with claim (if any), which marks what
// the message answers. followUp, if set, binds the first reply from the
// recipient to one background follow-up job (see initialState).
func (s *store) addOutbox(env envelope.Envelope, in envelope.Inner, followUp string, claim func(tx *sql.Tx, replyID string) error, selected ...boundOutgoing) error {
	if len(selected) > 0 && selected[0].binding != nil && selected[0].binding.remote != nil && selected[0].binding.remote.Role == "origin" {
		b := selected[0].binding
		return s.addConvOutbox([]outCopy{{in: in, env: env, state: stateReceiverWaiting, recipientFP: selected[0].fingerprint}}, envelope.Inner{}, claim, "", b)
	}
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
	recipientKey := "" // the exact key this was sealed to, when the sender knows it (SendMessage)
	if len(selected) > 0 {
		recipientKey = selected[0].fingerprint
	}
	if _, err := tx.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at, reply_to, follow_up, status, target, agent_id, required_cap, recipient_fp)
		VALUES(?, ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''), nullif(?, ''))`,
		env.ID, env.To, in.Body, string(data), stateQueued, time.Now().Unix(), in.ReplyTo, followUp, in.Status, targetJSON(in.Target), in.AgentID, agentRequirement(in), recipientKey); err != nil {
		return err
	}
	if len(selected) > 0 && selected[0].binding != nil {
		if followUp != "" {
			return errors.New("explicit reply receiver cannot use legacy follow-up")
		}
		if err := bindReplyReceiver(tx, selected[0].binding, []outCopy{{in: in, env: env, recipientFP: selected[0].fingerprint}}); err != nil {
			return err
		}
	}
	if err := replyEndsReminder(tx, in.ReplyTo, in.Status); err != nil {
		return err
	}
	for _, a := range in.Attachments {
		if _, err := tx.Exec(`INSERT INTO sent_attachments(message_id, blob_id, name, size, sha256) VALUES(?, ?, ?, ?, ?)`,
			env.ID, a.Blob.ID, a.Name, a.Size, a.SHA256); err != nil {
			return err
		}
	}
	for _, b := range env.Blobs {
		if _, err := tx.Exec(`INSERT INTO uploads(blob_id, message_id, state) VALUES(?, ?, ?)`, b.ID, env.ID, protocol.BlobUploading); err != nil {
			return err
		}
	}
	return s.done(tx.Commit())
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

// setOutboxState records what became of a sent message. An output held back
// here (not_delivered) never goes back to be sent (queued, waiting) or
// failed; that it was handed over after all (a hand-over already under way)
// is recorded as reported.
func (s *store) setOutboxState(id, state, errText, path string) error {
	_, err := s.db.Exec(`UPDATE outbox SET state = ?, error = nullif(?, ''), path = coalesce(nullif(?, ''), path)
		WHERE id = ? AND NOT (state = ? AND ? IN (?, ?, ?))`,
		state, errText, path, id, stateNotDelivered, state, stateQueued, stateConvWaiting, stateFailed)
	return s.done(err)
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
	// A conversation message to a frozen (conflicting) person is not sent.
	rows, err := s.db.Query(`SELECT envelope FROM outbox WHERE state = ? AND (conv IS NULL OR recipient NOT IN
		(SELECT d.address FROM person_devices d JOIN persons p ON p.person = d.person WHERE p.state = ?)) ORDER BY created_at`, stateQueued, personConflict)
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

const insertInbox = `INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, reply_to, received_at, session, status, state, verified_by, target, agent_id, received_ms)
	VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?)`

// Response states of received questions and tasks. They are independent of
// read/unread: reading never makes anything run.
const (
	statePending   = "pending"   // eligible for the worker because the sender is approved
	stateAccepted  = "accepted"  // eligible for the worker because the local user accepted it
	stateHeld      = "held"      // question from a sender not approved for automatic answers
	stateConvHeld  = "conv_held" // a conversation (version 2) question or task for the person (no participation): never run
	stateAwaiting  = "awaiting"  // task waiting for the local human to accept
	stateRunning   = "running"   // the worker owns it
	stateCancelReq = "cancel_requested"
	stateAnswered  = "answered"  // the worker replied
	stateManual    = "manual"    // someone replied by hand
	stateDeclined  = "declined"  // the local human declined the task
	stateJobFailed = "failed"    // the worker ran and failed or timed out
	stateCancelled = "cancelled" // cancelled while running
	stateInterrupt = "interrupted"
	stateSummary   = "summarized"  // a follow-up job stored its summary in detail
	stateNeedHuman = "needs_human" // the responder said the local human must decide
	stateResolved  = "resolved"    // the local human dealt with a needs_human item

	// Requests to this device's agent in a DM (agentjob.go).
	stateAgentWaiting = "part_waiting"  // waits until it may run (claim-time authority)
	stateNotRun       = "not_run"       // never runs: its participation ended, or it is not for this agent
	stateNotDelivered = "not_delivered" // it ran, but its output was held back (also an outbox state); kept here
)

// reviewStates are the states that wait for the local human's decision.
var reviewStates = []any{stateHeld, stateAwaiting, stateNeedHuman, stateConvHeld}

const inReview = `state IN (?, ?, ?, ?) AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs ri WHERE ri.inbox_id=inbox.id)`

// alertReviewStates are the review states that ask for attention by the
// legacy desktop review notification and the review notice to another
// agent: a DM question or task for the person (stateConvHeld) is not among
// them, as it is a person's DM turn, which follows the DM's own opt-in
// alerts (alerts.go). It stays in review (reviewStates) all the same. A
// request to this device's agent that needs the person (awaiting,
// needs_human) keeps them.
var alertReviewStates = []any{stateHeld, stateAwaiting, stateNeedHuman}

const inAlertReview = `state IN (?, ?, ?) AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs ri WHERE ri.inbox_id=inbox.id)`

// inboxArgs are insertInbox's arguments. The arrival is kept to the
// millisecond too, as a conversation message's is, so that the inbox lists
// both kinds in the order they arrived.
func inboxArgs(in envelope.Inner, state, verifiedBy string) []any {
	now := time.Now()
	return []any{in.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, now.Unix(), in.Session, in.Status, state, verifiedBy, targetJSON(in.Target), in.AgentID, now.UnixMilli()}
}

// initialState decides whether a new message waits for anything.
// verifiedBy is the fingerprint of the key that verified in ("" if unknown).
func initialState(db querier, in envelope.Inner, verifiedBy string) (string, error) {
	if route := in.ReceiverRoute; route != nil && route.Op != "request" {
		if route.Op == "delegate" {
			return stateAwaiting, nil // local setup approval, never a model task
		}
		return stateNotRun, nil // catalog and ready carry no execution request
	}
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
		granted, err := taskGranted(db, in.From, verifiedBy)
		if err != nil || !granted {
			return stateAwaiting, err
		}
		return statePending, nil
	case envelope.KindMessage:
		if isReviewNotice(in) {
			return stateNeedHuman, nil
		}
	}
	return "", nil
}

// isReviewNotice reports whether in is exactly a review notice (see
// envelope.StatusReviewNotice). Its body is never parsed.
func isReviewNotice(in envelope.Inner) bool {
	return in.Kind == envelope.KindMessage && in.Status == envelope.StatusReviewNotice &&
		in.ReplyTo == "" && len(in.Attachments) == 0
}

// bindFollowUp makes a reply to a request sent with a follow-up eligible for
// the worker. Only a reply from that request's recipient counts, and only the
// first: the outbox row records which reply it went to, so further replies
// (or the same reply under another id) start nothing.
func bindFollowUp(tx *sql.Tx, in envelope.Inner) error {
	if in.ReplyTo == "" || in.Kind == envelope.KindQuestion || in.Kind == envelope.KindTask || isResponderProgress(in) {
		return nil
	}
	res, err := tx.Exec(`UPDATE outbox SET followed_by = ? WHERE id = ? AND recipient = ? AND follow_up IS NOT NULL AND followed_by IS NULL`,
		in.ID, in.ReplyTo, in.From)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		_, err = tx.Exec(`UPDATE inbox SET state = ? WHERE id = ?`, statePending, in.ID)
	}
	return err
}

// followUp returns the follow-up instructions bound to reply id.
func (s *store) followUp(replyID string) (string, error) {
	var text string
	err := s.db.QueryRow(`SELECT follow_up FROM outbox WHERE followed_by = ?`, replyID).Scan(&text)
	return text, err
}

type querier interface {
	QueryRow(query string, args ...any) *sql.Row
}

// dbq reads the store, directly or within a transaction.
type dbq interface {
	querier
	Query(query string, args ...any) (*sql.Rows, error)
}

// insertInner stores a verified message and its attachment manifest.
// verifiedBy is the fingerprint of the key the caller verified it with.
func insertInner(tx *sql.Tx, in envelope.Inner, verifiedBy string) error {
	state, err := initialState(tx, in, verifiedBy)
	if err != nil {
		return err
	}
	in = tombstoned(tx, in, verifiedBy) // a message deleted before it arrived here keeps no text
	res, err := tx.Exec(insertInbox, inboxArgs(in, state, verifiedBy)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil // already stored
	}
	if in.ReceiverRoute != nil {
		if _, err := tx.Exec(`UPDATE inbox SET receiver_route=? WHERE id=?`, receiverRouteJSON(in.ReceiverRoute), in.ID); err != nil {
			return err
		}
	}
	if state == stateNeedHuman { // a review notice: say locally what it is
		if _, err := tx.Exec(`UPDATE inbox SET detail = ? WHERE id = ?`, reviewNoticeDetail(in.From), in.ID); err != nil {
			return err
		}
	}
	// The inbox_arrival trigger numbers the row (see schema step 8), for
	// this and any older writer alike.
	if err := bindFollowUp(tx, in); err != nil {
		return err
	}
	if err := bindReplyReceiverInput(tx, in, verifiedBy); err != nil {
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

// addInbox stores in, which the key with fingerprint verifiedBy verified.
func (s *store) addInbox(in envelope.Inner, verifiedBy string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertInner(tx, in, verifiedBy); err != nil {
		return err
	}
	return s.done(tx.Commit())
}

// quarantine holds an envelope that failed verification, keyed by its id.
func (s *store) quarantine(id, sender, reason string, raw []byte) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO quarantine(id, sender, reason, envelope, received_at) VALUES(?, ?, ?, ?, ?)`,
		id, sender, reason, string(raw), time.Now().Unix())
	return s.done(err)
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
func (s *store) promote(in envelope.Inner, verifiedBy string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertInner(tx, in, verifiedBy); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, in.ID); err != nil {
		return err
	}
	return s.done(tx.Commit())
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
	ID          string           `json:"id"`
	From        string           `json:"from"`
	Kind        string           `json:"kind"`
	State       string           `json:"state,omitempty"`  // response state of a question or task
	Status      string           `json:"status,omitempty"` // outcome carried by an answer or result
	Responder   string           `json:"responder,omitempty"`
	AgentID     string           `json:"agent_id,omitempty"`
	Target      *envelope.Target `json:"target,omitempty"`
	Detail      string           `json:"detail,omitempty"`
	Sub         string           `json:"sub,omitempty"` // a conversation record's subtype: "event" is a participation event (its signed JSON is the body)
	Body        string           `json:"body"`
	ReplyTo     string           `json:"reply_to,omitempty"`
	SentAt      time.Time        `json:"sent_at"`
	ReceivedAt  time.Time        `json:"received_at"`
	Read        bool             `json:"read"`
	Attachments []FileInfo       `json:"attachments,omitempty"`
}

// FileInfo describes a received attachment from its encrypted manifest.
type FileInfo struct {
	BlobID    string `json:"blob_id"`
	Name      string `json:"name"` // as the sender named it; not a safe path
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	SavedPath string `json:"saved_path,omitempty"`
	// A file known from history only: "requestable" (ask another device of
	// this person for it: RequestFile), "requested", or "unavailable" (no
	// device of this person could provide it). Empty: here, or fetched.
	Availability string `json:"availability,omitempty"`
	// Openable says this device can open the file now (OpenFile): a
	// received file it holds or can fetch, or a sent file it kept a copy
	// of. False for a sent file this device kept no copy of (sent before
	// copies were kept, or from another device) and for history-only files.
	Openable bool `json:"openable"`

	ctSize   int64
	ctSHA256 string
}

// recordSubs are the received records between devices that are never a
// message: group proofs, contexts, invitations, consents and withdrawals,
// and Drive space records (a conversation's view leaves them out too).
const recordSubs = `('drive-space', 'group-proof', 'group-context', 'group-invite', 'group-consent', 'group-withdrawal')`

// inbox lists received messages; a local request to this device's own
// agent (agentjob.go) is not one, nor is a record between devices.
func (s *store) inbox(unreadOnly bool) ([]Message, error) {
	where := ` WHERE local = 0 AND ref_id IS NULL AND coalesce(sub, '') NOT IN ` + recordSubs
	if unreadOnly {
		where += ` AND read_at IS NULL`
	}
	return s.messages(where)
}

// inboxMessage returns one received message, or nil if there is none.
func (s *store) inboxMessage(id string) (*Message, error) {
	msgs, err := s.messages(` WHERE id = ?`, id)
	if err != nil || len(msgs) == 0 {
		return nil, err
	}
	return &msgs[0], nil
}

func (s *store) messages(where string, args ...any) ([]Message, error) {
	q := `SELECT id, sender, kind, body, coalesce(reply_to, ''), ts, received_at, read_at IS NOT NULL,
		state, coalesce(status, ''), coalesce(responder, ''), coalesce(detail, ''), coalesce(agent_id, ''), coalesce(target, ''), coalesce(sub, '') FROM inbox`
	// In arrival order: to the millisecond where it is known, then as
	// stored (an id is random, so it never orders one second's arrivals).
	rows, err := s.db.Query(q+where+` ORDER BY coalesce(received_ms, received_at * 1000), rowid`, args...)
	if err != nil {
		return nil, err
	}
	var out []Message
	for rows.Next() {
		var m Message
		var ts, recv int64
		var target string
		if err := rows.Scan(&m.ID, &m.From, &m.Kind, &m.Body, &m.ReplyTo, &ts, &recv, &m.Read,
			&m.State, &m.Status, &m.Responder, &m.Detail, &m.AgentID, &target, &m.Sub); err != nil {
			rows.Close()
			return nil, err
		}
		if target != "" {
			if err := json.Unmarshal([]byte(target), &m.Target); err != nil {
				rows.Close()
				return nil, err
			}
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
	return s.done(err)
}

func (s *store) markRead(ids []string) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE inbox SET read_at = ? WHERE id = ? AND read_at IS NULL`, time.Now().Unix(), id); err != nil {
			return err
		}
	}
	return s.done(nil)
}

// markRecordsRead marks read the records between devices that the inbox
// leaves out (recordSubs): none is a message to read, and listing the
// inbox cleared them when it still listed them.
func (s *store) markRecordsRead() error {
	_, err := s.db.Exec(`UPDATE inbox SET read_at = ? WHERE read_at IS NULL AND local = 0 AND ref_id IS NULL AND coalesce(sub, '') IN `+recordSubs, time.Now().Unix())
	return s.done(err)
}

func (s *store) inboxKind(id string) (sender, kind string, err error) {
	var conv bool
	err = s.db.QueryRow(`SELECT sender, kind, conv IS NOT NULL FROM inbox WHERE id = ?`, id).Scan(&sender, &kind, &conv)
	if err == nil && conv {
		err = ErrConversationItem
	}
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
	ID, From, Kind, Body, ReplyTo, Status string
	Attachments                           int

	AgentID  string
	Executor *ExecutorStamp
	Receiver *ReplyReceiverBinding // explicit local continuation, separate from wire author

	// A request to this device's agent in a DM (agentjob.go).
	Conv, PID, Key string // Key: the fingerprint that verified it
	Target         *envelope.Target
	Local          bool // asked here, by this device's own person
}

// followUp reports whether j processes a reply to one of our requests,
// rather than answering a question or running a task.
func (j job) followUp() bool {
	return j.Kind != envelope.KindQuestion && j.Kind != envelope.KindTask
}

// claimJob gives the oldest eligible question or task to the worker,
// recording which responder took it. Only one caller can win a row. A
// question or task is eligible if the local user accepted it, or if it is
// pending and, at claim time, its sender is still approved (question) or
// still holds a task grant for the key that verified it (task).
func (s *store) claimJob(responder string, resolve ...func(dbq, string) (*ExecutorStamp, error)) (job, bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return job{}, false, err
	}
	defer tx.Rollback()
	changed := false
	for {
		var j job
		var target string
		err = tx.QueryRow(`UPDATE inbox SET state = ?, responder = ?, detail = NULL,
   attempts = attempts + 1, last_attempt_at = unixepoch()
   WHERE id = (SELECT id FROM inbox WHERE conv IS NULL AND replica = 0 AND (receiver_route IS NULL OR json_extract(receiver_route,'$.op')='request') AND NOT EXISTS (SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id) AND (state = ?
    OR (state = ? AND (kind NOT IN (?, ?) OR (kind = ? AND sender IN (SELECT address FROM approvals))
     OR (kind = ? AND `+taskGrantHolds+`))))
    AND (? != '' OR coalesce(json_extract(target, '$.agent_id'),'') != '')
    ORDER BY received_at, id LIMIT 1)
   RETURNING id,sender,kind,body,coalesce(reply_to,''),coalesce(status,''),coalesce(target,'')`,
			stateRunning, responder, stateAccepted, statePending, envelope.KindQuestion, envelope.KindTask,
			envelope.KindQuestion, envelope.KindTask, responder).Scan(&j.ID, &j.From, &j.Kind, &j.Body, &j.ReplyTo, &j.Status, &target)
		if errors.Is(err, sql.ErrNoRows) {
			if err = tx.Commit(); err != nil {
				return job{}, false, err
			}
			if changed {
				s.changed()
			}
			return job{}, false, nil
		}
		if err != nil {
			return job{}, false, err
		}
		changed = true
		if target != "" {
			j.Target = &envelope.Target{}
			if json.Unmarshal([]byte(target), j.Target) != nil {
				return job{}, false, errors.New("invalid stored execution target")
			}
			j.AgentID = j.Target.AgentID
		}
		var stamp *ExecutorStamp
		if len(resolve) > 0 {
			stamp, err = resolve[0](tx, j.AgentID)
		} else if j.AgentID != "" {
			err = ErrUnknownAgent
		}
		if err == nil && j.AgentID != "" && (stamp == nil || stamp.Record == nil || stamp.AgentID != j.AgentID || stamp.Record.Host != j.Target.Address || stamp.Record.HostKey != j.Target.Fingerprint) {
			err = ErrUnknownAgent
		}
		if err != nil && !errors.Is(err, ErrUnknownAgent) {
			return job{}, false, err
		}
		if err != nil || (len(resolve) > 0 && stamp == nil) {
			if err == nil {
				err = ErrUnknownAgent
			}
			if _, err = tx.Exec(`UPDATE inbox SET state=?, detail=? WHERE id=? AND state=?`, stateNotRun, "not run: selected agent unavailable", j.ID, stateRunning); err != nil {
				return job{}, false, err
			}
			continue // no fallback; a removed identity cannot starve the next job
		}
		if stamp != nil {
			raw, _ := json.Marshal(stamp)
			if _, err = tx.Exec(`UPDATE inbox SET executor=?,agent_id=?,responder=? WHERE id=? AND state=?`, string(raw), stamp.AgentID, stamp.Responder.Harness, j.ID, stateRunning); err != nil {
				return job{}, false, err
			}
			j.Executor = stamp
		}
		if err = tx.QueryRow(`SELECT count(*) FROM attachments WHERE message_id=?`, j.ID).Scan(&j.Attachments); err != nil {
			return job{}, false, err
		}
		if err = tx.Commit(); err != nil {
			return job{}, false, err
		}
		s.changed()
		return j, true, nil
	}
}

func (s *store) jobState(id string) (string, error) {
	var state string
	err := s.db.QueryRow(`SELECT state FROM inbox WHERE id = ?`, id).Scan(&state)
	return state, err
}

// finishJob records a job's end without a reply. A new needs_human
// outcome is notified afresh.
func (s *store) finishJob(id, state, detail string) error {
	res, err := s.db.Exec(`UPDATE inbox SET state = ?, detail = nullif(?, ''), notified = 0, review_sent = 0 WHERE id = ? AND state IN (?, ?)`,
		state, detail, id, stateRunning, stateCancelReq)
	if err == nil {
		if n, _ := res.RowsAffected(); n == 1 {
			_, err = s.db.Exec(`DELETE FROM reported WHERE item = ?`, id) // back in review: reported afresh to each recipient
		}
	}
	return s.done(err)
}

// unnotified returns the ids of items waiting for this person's decision
// HERE that no desktop notification has covered yet, and how many such
// items wait in all. A person's DM turn is not one (alertReviewStates),
// and neither is a review notice received from another machine: that is a
// report about decisions waiting THERE (unnotifiedNotices), never one
// waiting here.
func (s *store) unnotified() (ids []string, total int, err error) {
	args := append(append([]any{}, alertReviewStates...), envelope.KindMessage, envelope.StatusReviewNotice)
	rows, err := s.db.Query(`SELECT id, notified FROM inbox WHERE `+inAlertReview+` AND NOT (`+receivedNotice+`)`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var notified bool
		if err := rows.Scan(&id, &notified); err != nil {
			return nil, 0, err
		}
		total++
		if !notified {
			ids = append(ids, id)
		}
	}
	return ids, total, rows.Err()
}

func (s *store) markNotified(ids []string) error {
	for _, id := range ids {
		if _, err := s.db.Exec(`UPDATE inbox SET notified = 1 WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return nil
}

// interruptRunning marks jobs a previous daemon left running. They are not
// rerun automatically: a task may already have had effects.
func (s *store) interruptRunning() error {
	_, err := s.db.Exec(`UPDATE inbox SET state = ?, detail = 'the daemon stopped while this was running'
		WHERE state IN (?, ?)`, stateInterrupt, stateRunning, stateCancelReq)
	return s.done(err)
}

// threadText returns up to max earlier messages of the conversation with
// peer that ends at replyTo (oldest first), following reply_to links through
// the inbox (messages from peer) and the outbox (messages to peer). It stops
// at any message that is not between this installation and peer, or whose
// earlier link is unknown, so a sender cannot pull in other conversations by
// naming their ids.
func (s *store) threadText(peer, replyTo string, max int) ([]string, error) {
	var out []string
	for id := replyTo; id != "" && len(out) < max; {
		var who, body, next, kind, state, status string
		err := s.db.QueryRow(`SELECT body, coalesce(reply_to, ''), kind, state, coalesce(status, '') FROM inbox WHERE id = ? AND sender = ?`, id, peer).Scan(&body, &next, &kind, &state, &status)
		who = peer
		if errors.Is(err, sql.ErrNoRows) {
			// v1 keeps its kind in the envelope; the kind column belongs to DMs.
			err = s.db.QueryRow(`SELECT body, coalesce(reply_to, ''), coalesce(json_extract(envelope, '$.kind'), ''), state, coalesce(status, '') FROM outbox WHERE id = ? AND recipient = ?`, id, peer).Scan(&body, &next, &kind, &state, &status)
			who = "me"
		}
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, err
		}
		label := kind + "; local state: " + state
		if status != "" {
			label += "; outcome: " + status
		}
		if d := s.legacyDisplay(id, peer); d.Deleted {
			body = "(deleted by its author)"
		} else if d.Edited {
			body, label = d.Text, label+"; edited"
		}
		out = append([]string{who + " [" + label + "]: " + body}, out...)
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

// sentRow is one message this agent sent, for projections such as A2A tasks.
type sentRow struct {
	ID, To, Body, State, Path, Envelope string
	Created                             int64
}

func (s *store) sent(id string) (sentRow, error) {
	var r sentRow
	err := s.db.QueryRow(`SELECT id, recipient, body, state, coalesce(path, ''), envelope, created_at FROM outbox WHERE id = ?`, id).
		Scan(&r.ID, &r.To, &r.Body, &r.State, &r.Path, &r.Envelope, &r.Created)
	return r, err
}

// replyTo returns the first terminal answer/result or ordinary message from
// peer that replies to id. Nonterminal responder progress and replies from
// anyone else are ignored.
func (s *store) replyTo(id, peer string) (string, error) {
	var reply string
	err := s.db.QueryRow(`SELECT id FROM inbox WHERE reply_to = ? AND sender = ? AND kind IN (?, ?, ?) AND coalesce(status, '') != ? ORDER BY received_at, id LIMIT 1`,
		id, peer, envelope.KindAnswer, envelope.KindResult, envelope.KindMessage, envelope.StatusProgress).Scan(&reply)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return reply, err
}
