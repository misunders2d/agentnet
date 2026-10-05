package hub

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

// schema lists the SQL steps from each version to the next.
var schema = []string{`
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
`, `
CREATE TABLE blobs(
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  recipient TEXT NOT NULL,
  size INTEGER NOT NULL,
  sha256 TEXT NOT NULL,
  received INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,
  message_id TEXT,
  updated_at INTEGER NOT NULL);
CREATE INDEX blobs_state ON blobs(state, updated_at);
`, `
ALTER TABLE messages ADD COLUMN session TEXT;
ALTER TABLE messages ADD COLUMN fallback INTEGER NOT NULL DEFAULT 0;
`, `
CREATE TABLE release(
  id INTEGER PRIMARY KEY CHECK (id = 1),
  version TEXT NOT NULL,
  url TEXT NOT NULL,
  note TEXT NOT NULL,
  set_by TEXT NOT NULL,
  set_at INTEGER NOT NULL);
`, `
ALTER TABLE agents ADD COLUMN person TEXT;
ALTER TABLE agents ADD COLUMN last_session TEXT;
CREATE TABLE caps(
  address TEXT NOT NULL,
  session TEXT NOT NULL,
  record TEXT NOT NULL,
  ts INTEGER NOT NULL,
  PRIMARY KEY(address, session));
`, `
CREATE TABLE notify_prefs(
  address TEXT PRIMARY KEY,
  enabled INTEGER NOT NULL,
  updated_at INTEGER NOT NULL);
CREATE TABLE notify_senders(
  address TEXT NOT NULL,
  sender TEXT NOT NULL,
  fingerprint TEXT NOT NULL,
  PRIMARY KEY(address, sender));
CREATE TABLE notify_mutes(
  address TEXT NOT NULL,
  channel TEXT NOT NULL,
  PRIMARY KEY(address, channel));
CREATE TABLE push_subs(
  address TEXT PRIMARY KEY,
  sub_id TEXT NOT NULL,
  endpoint TEXT NOT NULL,
  p256dh TEXT NOT NULL,
  auth TEXT NOT NULL,
  created_at INTEGER NOT NULL);
CREATE TABLE notify_pending(
  address TEXT NOT NULL,
  channel TEXT NOT NULL,
  sender TEXT NOT NULL,
  gen INTEGER NOT NULL,
  count INTEGER NOT NULL,
  last_msg TEXT NOT NULL,
  due_ms INTEGER NOT NULL,
  expires_ms INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(address, channel, sender));
CREATE INDEX notify_pending_due ON notify_pending(due_ms);
`, `
ALTER TABLE notify_pending ADD COLUMN channels TEXT NOT NULL DEFAULT '';
`, `
CREATE TABLE persons(
  person TEXT PRIMARY KEY,
  seq INTEGER NOT NULL,
  hash TEXT NOT NULL,
  record TEXT NOT NULL);
CREATE TABLE person_chain(
  person TEXT NOT NULL,
  seq INTEGER NOT NULL,
  hash TEXT NOT NULL,
  record TEXT NOT NULL,
  PRIMARY KEY(person, seq));
ALTER TABLE agents ADD COLUMN person_id TEXT;
ALTER TABLE agents ADD COLUMN linked INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN pending_person TEXT;
ALTER TABLE agents ADD COLUMN pending_inviter TEXT;
ALTER TABLE agents ADD COLUMN pending_until INTEGER;
ALTER TABLE agents ADD COLUMN pending_event TEXT;
ALTER TABLE agents ADD COLUMN revoked_reason TEXT;
CREATE INDEX agents_person ON agents(person_id) WHERE person_id IS NOT NULL;
CREATE INDEX agents_pending ON agents(pending_inviter) WHERE pending_person IS NOT NULL;
ALTER TABLE invites ADD COLUMN person TEXT;
ALTER TABLE invites ADD COLUMN offer TEXT;
`, `
CREATE TABLE realm(
  id INTEGER PRIMARY KEY CHECK (id = 1),
  realm_id TEXT,
  initialized INTEGER NOT NULL CHECK (initialized IN (0, 1)),
  CHECK ((initialized = 0 AND realm_id IS NULL) OR
         (initialized = 1 AND realm_id IS NOT NULL)));
INSERT INTO realm(id, initialized) VALUES(1, 0);
`, TeamSchema, driveStorageSchema, GroupHubSchema, agentCatalogSchema, receiptSchema, workspaceSchema, deviceAdminSchema, inviteHintsSchema}

// addressTakenError refuses a join for an enrolled (or revoked) address
// and names a free one to offer the person. The invite stays unused; the
// free name is not reserved, only checked again by the next join.
type addressTakenError struct{ address, free string }

func (e *addressTakenError) Error() string {
	return fmt.Sprintf("address %s is already enrolled; %s is free now (not reserved)", e.address, e.free)
}

func (e *addressTakenError) Unwrap() error { return errAddressTaken }

// freeAddress names a free address under label for a join that found
// address taken. It runs only after a collision, so ordinary joins keep
// the single indexed lookup.
func freeAddress(tx *sql.Tx, label, address string) (string, error) {
	rows, err := tx.Query(`SELECT address FROM agents WHERE label = ?`, label)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	taken := map[string]bool{}
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			return "", err
		}
		_, name, _ := protocol.SplitAddress(addr)
		taken[name] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	_, name, _ := protocol.SplitAddress(address)
	return protocol.Address(label, freeName(name, taken)), nil
}

// freeName returns the first NAME-N (N >= 2) not in taken, continuing an
// existing short -N suffix, shortened to stay a valid name. Suffixes of
// more than six digits are not continued, so N never overflows.
func freeName(name string, taken map[string]bool) string {
	base, n := name, 2
	if i := strings.LastIndexByte(name, '-'); i > 0 && len(name)-i-1 <= 6 {
		if k, err := strconv.Atoi(name[i+1:]); err == nil && k >= 1 && name[i+1] != '0' {
			base, n = name[:i], k+1
		}
	}
	for ; ; n++ {
		suffix := "-" + strconv.Itoa(n)
		b := base
		if max := protocol.MaxName - len(suffix); len(b) > max {
			b = strings.TrimRight(b[:max], "-")
		}
		if c := b + suffix; !taken[c] && protocol.ValidName(c) {
			return c
		}
	}
}

var (
	errInviteInvalid = errors.New("invite invalid, expired, or already used")
	errAddressTaken  = errors.New("address already enrolled")
	errNotFound      = errors.New("not found")
	errIDConflict    = errors.New("message id already used for different content")
	errBlobNotReady  = errors.New("attachment is not a completed upload for this recipient")
	errQuota         = errors.New("Hub storage quota exceeded")
	errBlobConflict  = errors.New("blob id already used for a different upload")
	errBlobBusy      = errors.New("upload is being reclaimed; retry later")
)

type store struct{ db *sql.DB }

func openStore(path string) (*store, error) {
	db, err := sqlitedb.Open(path, schema)
	if err != nil {
		return nil, err
	}
	return &store{db}, nil
}

type agent struct {
	Public        identity.Public
	Admin         bool // an admin invite made it one, or its person granted it (personadmin.go)
	Revoked       bool
	RevokedReason string // "refused" or "expired" for a device link nobody approved; "removed" from its person
	Pending       bool   // joined with a device invite, waiting for its person's approval: not a member
	PendingUntil  int64  // unix seconds: after this nobody can approve it any more
	Person        string // the person it speaks for, if any
	linked        bool
	grantedBy     string
}

func (s *store) agent(address string) (agent, error) { return agentIn(s.db, address) }

func agentIn(q interface {
	QueryRow(string, ...any) *sql.Row
}, address string) (agent, error) {
	a, err := rawAgentIn(q, address)
	if err == nil && a.Admin && a.linked {
		a.Admin, err = deviceAdminHolds(q, address, a, map[string]bool{})
	}
	return a, err
}

func rawAgentIn(q querier, address string) (agent, error) {
	var a agent
	var pub string
	var revoked sql.NullInt64
	var reason, pending, person, grantedBy sql.NullString
	var until sql.NullInt64
	err := q.QueryRow(`SELECT public, admin, revoked_at, revoked_reason, pending_person, pending_until, person_id, linked, admin_granted_by FROM agents WHERE address = ?`, address).
		Scan(&pub, &a.Admin, &revoked, &reason, &pending, &until, &person, &a.linked, &grantedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return a, errNotFound
	}
	if err != nil {
		return a, err
	}
	a.Revoked, a.RevokedReason, a.Pending, a.PendingUntil, a.Person = revoked.Valid, reason.String, pending.Valid, until.Int64, person.String
	a.grantedBy = grantedBy.String
	return a, json.Unmarshal([]byte(pub), &a.Public)
}

// enrolledMember is one unrevoked agent for the member list.
type enrolledMember struct {
	address string
	joined  int64               // unix seconds
	person  *protocol.PersonRef // the newest roster step of its person, if any
	agent   bool                // its newest caps record lists protocol.CapAgent (a hint)
}

// members lists up to limit unrevoked agents, most recently enrolled first,
// and whether more exist.
func (s *store) members(limit int) ([]enrolledMember, bool, error) {
	// The agent hint comes from the device's newest signed caps record,
	// not its last session's: a stream sets last_session on connect, before
	// that session publishes, and the hint must not flicker on reconnect.
	rows, err := s.db.Query(`SELECT a.address, a.created_at, p.person, p.seq, p.hash,
		(SELECT c.record FROM caps c WHERE c.address = a.address ORDER BY c.ts DESC, c.session LIMIT 1)
		FROM agents a LEFT JOIN persons p ON p.person = a.person_id
		WHERE a.revoked_at IS NULL AND a.pending_person IS NULL ORDER BY a.created_at DESC, a.rowid DESC LIMIT ?`, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []enrolledMember
	for rows.Next() {
		var m enrolledMember
		var person, hash, caps sql.NullString
		var seq sql.NullInt64
		if err := rows.Scan(&m.address, &m.joined, &person, &seq, &hash, &caps); err != nil {
			return nil, false, err
		}
		if caps.Valid {
			rec, err := protocol.ParseCapsRecord([]byte(caps.String))
			m.agent = err == nil && rec.Has(protocol.CapAgent) // unreadable: no hint
		}
		if person.Valid {
			m.person = &protocol.PersonRef{ID: person.String, Seq: seq.Int64, Hash: hash.String}
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(out) > limit {
		return out[:limit], true, nil
	}
	return out, false, nil
}

// setLastSession records the session that connected last for address, so
// an offline device keeps what that session could read.
func (s *store) setLastSession(address, session string) error {
	_, err := s.db.Exec(`UPDATE agents SET last_session = ? WHERE address = ?`, session, address)
	return err
}

// maxCapsSessions bounds the capability records kept per device.
const maxCapsSessions = 8

// putCaps stores a session's capability record unless a newer one is
// stored for that session, and keeps only the newest records per device.
func (s *store) putCaps(address, session string, ts int64, record []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO caps(address, session, record, ts) VALUES(?, ?, ?, ?)
		ON CONFLICT(address, session) DO UPDATE SET record = excluded.record, ts = excluded.ts WHERE excluded.ts > caps.ts`,
		address, session, string(record), ts); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM caps WHERE address = ? AND session NOT IN
		(SELECT session FROM caps WHERE address = ? ORDER BY ts DESC, session LIMIT ?)
		AND session IS NOT (SELECT last_session FROM agents WHERE address = ?)`,
		address, address, maxCapsSessions, address); err != nil {
		return err
	}
	return tx.Commit()
}

// profileRows returns address's published person roster, last connected
// session and the capability records of sessions.
func (s *store) profileRows(address string, sessions []string) (person, last string, caps map[string]string, err error) {
	var p, l sql.NullString
	if err = s.db.QueryRow(`SELECT p.record, a.last_session FROM agents a LEFT JOIN persons p ON p.person = a.person_id
		WHERE a.address = ? AND a.revoked_at IS NULL AND a.pending_person IS NULL`, address).Scan(&p, &l); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errNotFound
		}
		return
	}
	person, last, caps = p.String, l.String, map[string]string{}
	if len(sessions) == 0 && last != "" {
		sessions = []string{last}
	}
	for _, id := range sessions {
		var rec string
		switch err = s.db.QueryRow(`SELECT record FROM caps WHERE address = ? AND session = ?`, address, id).Scan(&rec); {
		case err == nil:
			caps[id] = rec
		case errors.Is(err, sql.ErrNoRows):
			err = nil
		default:
			return
		}
	}
	return
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
// It reports whether the invite was the Hub-issued bootstrap invite, and
// for a device invite the inviting device (the new one is then PENDING:
// linkPending).
func (s *store) enroll(secret string, pub identity.Public, label string, link *protocol.JoinLink) (bootstrap bool, inviter string, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, "", err
	}
	defer tx.Rollback()
	data, err := json.Marshal(pub)
	if err != nil {
		return false, "", err
	}
	var invLabel string
	var admin bool
	var expires int64
	var createdBy, usedBy, person, offer sql.NullString
	err = tx.QueryRow(`SELECT label, admin, expires_at, created_by, used_by, person, offer FROM invites WHERE secret_hash = ?`,
		protocol.HashSecret(secret)).Scan(&invLabel, &admin, &expires, &createdBy, &usedBy, &person, &offer)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && invLabel != label) {
		return false, "", errInviteInvalid
	}
	if err != nil {
		return false, "", err
	}
	if person.Valid != (link != nil) || person.Valid && link.Offer != offer.String {
		return false, "", errInviteInvalid // a device invite needs its link; no other invite takes one
	}
	if usedBy.Valid {
		var stored string
		var revoked sql.NullInt64
		err := tx.QueryRow(`SELECT public, revoked_at FROM agents WHERE address = ?`, usedBy.String).Scan(&stored, &revoked)
		if err == nil && usedBy.String == pub.Address && stored == string(data) && !revoked.Valid {
			return !createdBy.Valid, createdBy.String, nil // exact replay
		}
		return false, "", errInviteInvalid
	}
	if expires <= time.Now().Unix() {
		return false, "", errInviteInvalid
	}
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM agents WHERE address = ?`, pub.Address).Scan(&exists); err != nil {
		return false, "", err
	}
	if exists > 0 { // revoked addresses stay taken
		free, err := freeAddress(tx, label, pub.Address)
		if err != nil {
			return false, "", err
		}
		return false, "", &addressTakenError{address: pub.Address, free: free}
	}
	if person.Valid {
		if err := enrollPending(tx, person.String, createdBy.String, pub, data, label, link, expires); err != nil {
			return false, "", err
		}
	} else if _, err := tx.Exec(`INSERT INTO agents(address, label, public, admin, created_at) VALUES(?, ?, ?, ?, ?)`,
		pub.Address, label, string(data), admin, time.Now().Unix()); err != nil {
		return false, "", err
	}
	if _, err := tx.Exec(`UPDATE invites SET used_by = ? WHERE secret_hash = ?`, pub.Address, protocol.HashSecret(secret)); err != nil {
		return false, "", err
	}
	return !createdBy.Valid, createdBy.String, tx.Commit()
}

func (s *store) revoke(address string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE agents SET revoked_at = ? WHERE address = ? AND revoked_at IS NULL`, time.Now().Unix(), address)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNotFound
	}
	if err := revokeDeviceAdminGrants(tx, address); err != nil {
		return err
	}
	if err := dropNotify(tx, address); err != nil { // its notification state goes with it
		return err
	}
	return tx.Commit()
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

// putMessage stores an envelope once, attaching its blobs, which must be
// complete uploads by the sender for the same recipient with the signed size
// and digest. A retry with identical bytes returns the existing state; reuse
// of the id for other content is a conflict.
func (s *store) putMessage(env envelope.Envelope, canonical []byte, senderFP string, now time.Time) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var existing []byte
	var state string
	err = tx.QueryRow(`SELECT envelope, state FROM messages WHERE id = ?`, env.ID).Scan(&existing, &state)
	if err == nil {
		if string(existing) != string(canonical) {
			return "", errIDConflict
		}
		return state, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	for _, b := range env.Blobs {
		res, err := tx.Exec(`UPDATE blobs SET message_id = ? WHERE id = ? AND owner = ? AND recipient = ?
			AND size = ? AND sha256 = ? AND state = ? AND message_id IS NULL`,
			env.ID, b.ID, env.From, env.To, b.Size, b.SHA256, protocol.BlobStored)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return "", fmt.Errorf("%w: %s", errBlobNotReady, b.ID)
		}
	}
	if _, err := tx.Exec(`INSERT INTO messages(id, sender, recipient, envelope, state, created_at, session, fallback) VALUES(?, ?, ?, ?, ?, ?, nullif(?, ''), ?)`,
		env.ID, env.From, env.To, canonical, protocol.StateCustody, now.Unix(), env.Session, env.Fallback); err != nil {
		return "", err
	}
	if env.Attn { // stored with the message, or not at all (notify.go)
		if err := enqueueAttention(tx, env, senderFP, now); err != nil {
			return "", err
		}
	}
	return protocol.StateCustody, tx.Commit()
}

type pending struct {
	Seq      int64
	Envelope []byte
}

// pendingFor lists undelivered messages for one connection of recipient:
// those for the agent, for this session, or for any session with fallback.
func (s *store) pendingFor(recipient, session string, afterSeq int64) ([]pending, error) {
	rows, err := s.db.Query(`SELECT seq, envelope FROM messages
		WHERE recipient = ? AND state = ? AND seq > ? AND (session IS NULL OR session = ? OR fallback = 1)
		ORDER BY seq LIMIT 100`,
		recipient, protocol.StateCustody, afterSeq, session)
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
func (s *store) setDisposition(id, recipient, state string) (current, sender string, changed bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE messages SET state = ?, delivered_at = coalesce(delivered_at, ?) WHERE id = ? AND recipient = ? AND (state = ? OR (state = ? AND ? = ?))`, state, time.Now().Unix(), id, recipient, protocol.StateCustody, protocol.StateQuarantined, state, protocol.StateDelivered)
	if err != nil {
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		return
	}
	changed = n == 1
	err = tx.QueryRow(`SELECT state, sender FROM messages WHERE id = ? AND recipient = ?`, id, recipient).Scan(&current, &sender)
	if errors.Is(err, sql.ErrNoRows) {
		err = errNotFound
	}
	if err != nil {
		return
	}
	if changed {
		_, err = tx.Exec(`INSERT INTO receipts(sender,id,state,seq) SELECT ?,?,?,coalesce(max(seq),0)+1 FROM receipts WHERE sender=?`, sender, id, current, sender)
		if err != nil {
			return
		}
	}
	err = tx.Commit()
	return
}

type blobRow struct {
	Owner, Recipient string
	Size, Received   int64
	SHA256, State    string
	Attached         bool
}

func (s *store) blob(id string) (blobRow, error) {
	var b blobRow
	var msg sql.NullString
	err := s.db.QueryRow(`SELECT owner, recipient, size, received, sha256, state, message_id FROM blobs WHERE id = ?`, id).
		Scan(&b.Owner, &b.Recipient, &b.Size, &b.Received, &b.SHA256, &b.State, &msg)
	if errors.Is(err, sql.ErrNoRows) {
		return b, errNotFound
	}
	b.Attached = msg.Valid
	return b, err
}

// reserveBlob creates an upload, or returns the existing one if the request
// repeats it exactly. The quota check and insert share one write
// transaction, so concurrent reservations cannot oversubscribe it. Rows
// being reclaimed still count toward the quota until their files are gone.
func (s *store) reserveBlob(owner string, r protocol.BlobReserve, quota int64) (blobRow, error) {
	var b blobRow
	tx, err := s.db.Begin()
	if err != nil {
		return b, err
	}
	defer tx.Rollback()
	var msg sql.NullString
	err = tx.QueryRow(`SELECT owner, recipient, size, received, sha256, state, message_id FROM blobs WHERE id = ?`, r.ID).
		Scan(&b.Owner, &b.Recipient, &b.Size, &b.Received, &b.SHA256, &b.State, &msg)
	switch {
	case err == nil:
		if b.Owner != owner || b.Recipient != r.Recipient || b.Size != r.Size || b.SHA256 != r.SHA256 {
			return b, errBlobConflict
		}
		if b.State == blobReclaiming {
			return b, errBlobBusy
		}
		b.Attached = msg.Valid
		return b, tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return b, err
	}
	var used int64
	if err := tx.QueryRow(`SELECT coalesce(sum(size), 0) FROM blobs`).Scan(&used); err != nil {
		return b, err
	}
	if used+r.Size > quota {
		return b, errQuota
	}
	if _, err := tx.Exec(`INSERT INTO blobs(id, owner, recipient, size, sha256, state, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		r.ID, owner, r.Recipient, r.Size, r.SHA256, protocol.BlobUploading, time.Now().Unix()); err != nil {
		return b, err
	}
	b = blobRow{Owner: owner, Recipient: r.Recipient, Size: r.Size, SHA256: r.SHA256, State: protocol.BlobUploading}
	return b, tx.Commit()
}

// blobReclaiming marks an abandoned upload whose files are being deleted. The
// row (and its quota share) stays until the files are really gone, so a crash
// or failed delete is retried rather than leaking unaccounted disk.
const blobReclaiming = "reclaiming"

// markReclaiming flags incomplete uploads idle since before and returns
// every row awaiting reclamation, including ones left by earlier attempts.
// Completed uploads are never reclaimed.
func (s *store) markReclaiming(before time.Time) ([]string, error) {
	if _, err := s.db.Exec(`UPDATE blobs SET state = ? WHERE state = ? AND updated_at < ?`,
		blobReclaiming, protocol.BlobUploading, before.Unix()); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id FROM blobs WHERE state = ?`, blobReclaiming)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *store) deleteReclaimed(id string) error {
	_, err := s.db.Exec(`DELETE FROM blobs WHERE id = ? AND state = ?`, id, blobReclaiming)
	return err
}

// mustAffectOne turns "no such row" into errNotFound.
func mustAffectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errNotFound
	}
	return nil
}

func (s *store) setReceived(id string, received int64) error {
	return mustAffectOne(s.db.Exec(`UPDATE blobs SET received = ?, updated_at = ? WHERE id = ? AND state = ?`,
		received, time.Now().Unix(), id, protocol.BlobUploading))
}

// setBlobState moves an upload to stored (complete) or back to uploading
// from zero (after a digest mismatch).
func (s *store) setBlobState(id, state string) error {
	return mustAffectOne(s.db.Exec(`UPDATE blobs SET state = ?, received = CASE WHEN ? = ? THEN size ELSE 0 END, updated_at = ?
		WHERE id = ? AND state IN (?, ?)`,
		state, state, protocol.BlobStored, time.Now().Unix(), id, protocol.BlobUploading, protocol.BlobStored))
}

// expireSession marks undelivered messages addressed only to an ended
// session as expired, so their senders learn they were not delivered.
func (s *store) expireSession(recipient, session string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT DISTINCT sender FROM messages WHERE recipient=? AND session=? AND fallback=0 AND state=?`, recipient, session, protocol.StateCustody)
	if err != nil {
		return nil, err
	}
	var senders []string
	for rows.Next() {
		var sender string
		if err = rows.Scan(&sender); err != nil {
			rows.Close()
			return nil, err
		}
		senders = append(senders, sender)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO receipts(sender,id,state,seq) SELECT m.sender,m.id,?,coalesce((SELECT max(r.seq) FROM receipts r WHERE r.sender=m.sender),0)+row_number() OVER (PARTITION BY m.sender ORDER BY m.seq) FROM messages m WHERE m.recipient=? AND m.session=? AND m.fallback=0 AND m.state=?`, protocol.StateExpired, recipient, session, protocol.StateCustody)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE messages SET state=? WHERE recipient=? AND session=? AND fallback=0 AND state=?`, protocol.StateExpired, recipient, session, protocol.StateCustody)
	if err != nil {
		return nil, err
	}
	return senders, tx.Commit()
}

const receiptSchema = `CREATE TABLE receipts(seq INTEGER NOT NULL, sender TEXT NOT NULL, id TEXT NOT NULL, state TEXT NOT NULL, PRIMARY KEY(sender,seq));`

func (s *store) receiptMax(sender string) (n int64, err error) {
	err = s.db.QueryRow(`SELECT coalesce(max(seq),0) FROM receipts WHERE sender=?`, sender).Scan(&n)
	return
}
func (s *store) receiptsFor(sender string, cursor int64) ([]protocol.ReceiptEvent, error) {
	rows, err := s.db.Query(`SELECT id,state,seq FROM receipts WHERE sender=? AND seq>? ORDER BY seq LIMIT ?`, sender, cursor, protocol.ReceiptBatch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.ReceiptEvent
	for rows.Next() {
		var r protocol.ReceiptEvent
		if err = rows.Scan(&r.ID, &r.State, &r.Seq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
