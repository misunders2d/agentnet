package client

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Local state for human DMs: persons (this installation's own and pinned
// ones), conversation roots, and conversation messages in the inbox and
// outbox (schema step 13).

// Person states.
const (
	personSelf     = "self"     // created on this installation
	personPinned   = "pinned"   // verified against its device's key and pinned
	personConflict = "conflict" // a different record was seen for it: frozen
)

// Quarantine reasons of conversation messages.
const (
	reasonProof     = "proof_pending"         // conversation or member proof not complete yet; retried on new evidence
	reasonConflict  = "identity_conflict"     // a person record conflicts with a pinned one: frozen
	reasonDuplicate = "conflicting_duplicate" // same sender key and logical id as a stored message, other content
)

// stateConvWaiting is an outbox state: a conversation message kept, not
// sent, because the recipient's device cannot read conversations now. It
// is released when that changes; it is never sent as version 1.
const stateConvWaiting = "waiting"

// ErrConversationItem refuses a legacy action (accept, reply, decline) on a
// conversation message: nothing runs it until agent participation exists,
// and it is answered in its conversation.
var ErrConversationItem = errors.New("this message belongs to a conversation: answer it there (agentnet dm send); nothing runs it automatically yet")

var errPersonConflict = errors.New("this person's record conflicts with the one pinned here; it is frozen")

// PersonInfo is a person as this installation knows it.
type PersonInfo struct {
	Person      string `json:"person"`
	Label       string `json:"label"` // the person's own claim, not verified
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
	Roster      string `json:"roster"` // hash of the pinned roster
	State       string `json:"state"`  // self, pinned or conflict
}

type personRow struct {
	info   PersonInfo
	roster protocol.PersonRoster
	raw    []byte
	pub    *identity.Public // the device key it was verified against when pinned (nil for rows pinned before that was kept)
}

func scanPerson(row interface{ Scan(...any) error }) (personRow, bool, error) {
	var p personRow
	var raw, public string
	err := row.Scan(&p.info.Person, &p.info.Address, &p.info.Fingerprint, &p.info.Roster, &raw, &p.info.Label, &p.info.State, &public)
	if errors.Is(err, sql.ErrNoRows) {
		return p, false, nil
	}
	if err != nil {
		return p, false, err
	}
	p.raw = []byte(raw)
	if err := json.Unmarshal(p.raw, &p.roster); err != nil {
		return p, false, err
	}
	if public != "" {
		p.pub = &identity.Public{}
		if err := json.Unmarshal([]byte(public), p.pub); err != nil {
			return p, false, err
		}
	}
	return p, true, nil
}

const personCols = `person, address, fingerprint, hash, roster, label, state, coalesce(public, '')`

func (s *store) selfPerson() (personRow, bool, error) {
	return scanPerson(s.db.QueryRow(`SELECT `+personCols+` FROM persons WHERE state = ?`, personSelf))
}

func (s *store) personByID(id string) (personRow, bool, error) { return personByIDIn(s.db, id) }

func personByIDIn(q querier, id string) (personRow, bool, error) {
	return scanPerson(q.QueryRow(`SELECT `+personCols+` FROM persons WHERE person = ?`, id))
}

func (s *store) personByAddress(address string) (personRow, bool, error) {
	return scanPerson(s.db.QueryRow(`SELECT `+personCols+` FROM persons WHERE address = ?`, address))
}

// setSelfPerson stores this installation's own person; there is at most one.
func (s *store) setSelfPerson(r protocol.PersonRoster, raw []byte) error {
	d := r.Devices[0]
	_, err := s.db.Exec(`INSERT INTO persons(person, address, fingerprint, hash, roster, label, state, pinned_at)
		SELECT ?, ?, ?, ?, ?, ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM persons WHERE state = ? OR address = ?)`,
		r.Person, d.Address, d.Fingerprint, r.Hash(), string(raw), r.Label, personSelf, time.Now().Unix(), personSelf, d.Address)
	return s.done(err)
}

// pinPerson pins a roster verified against pub, keeping pub to check later
// records against. A different record for a pinned person id, or another
// person for a pinned address, freezes the pinned one as a conflict
// (errPersonConflict); nothing is replaced.
func (s *store) pinPerson(r protocol.PersonRoster, raw []byte, pub identity.Public) error {
	d := r.Devices[0]
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var hash, state string
	err = tx.QueryRow(`SELECT hash, state FROM persons WHERE person = ?`, r.Person).Scan(&hash, &state)
	switch {
	case err == nil && hash == r.Hash() && state != personConflict:
		return nil
	case err == nil:
		if state == personSelf {
			return errPersonConflict // someone else's record claims this installation's person
		}
		if _, err := tx.Exec(`UPDATE persons SET state = ?, conflict = coalesce(conflict, ?) WHERE person = ?`, personConflict, string(raw), r.Person); err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.changed()
		return errPersonConflict
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	var other string
	err = tx.QueryRow(`SELECT person, state FROM persons WHERE address = ?`, d.Address).Scan(&other, &state)
	switch {
	case err == nil:
		if state != personSelf {
			if _, err := tx.Exec(`UPDATE persons SET state = ?, conflict = coalesce(conflict, ?) WHERE person = ?`, personConflict, string(raw), other); err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return err
			}
			s.changed()
		}
		return errPersonConflict
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	public, _ := json.Marshal(pub)
	if _, err := tx.Exec(`INSERT INTO persons(person, address, fingerprint, hash, roster, label, state, pinned_at, public) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Person, d.Address, d.Fingerprint, r.Hash(), string(raw), r.Label, personPinned, time.Now().Unix(), string(public)); err != nil {
		return err
	}
	return s.done(tx.Commit())
}

// ConversationInfo is one conversation this installation holds.
type ConversationInfo struct {
	ID      string     `json:"id"`
	Kind    string     `json:"kind"`
	Peer    PersonInfo `json:"peer"`    // the other member
	Created int64      `json:"created"` // the creator's claim, unix seconds
	Creator string     `json:"creator"` // the creating device
}

func (s *store) conversation(id string) (protocol.ConvRoot, []byte, bool, error) {
	return conversationIn(s.db, id)
}

func conversationIn(q querier, id string) (protocol.ConvRoot, []byte, bool, error) {
	var raw string
	err := q.QueryRow(`SELECT root FROM conversations WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.ConvRoot{}, nil, false, nil
	}
	if err != nil {
		return protocol.ConvRoot{}, nil, false, err
	}
	var root protocol.ConvRoot
	return root, []byte(raw), true, json.Unmarshal([]byte(raw), &root)
}

// addConversation pins a verified root; a pinned root is never replaced.
func (s *store) addConversation(root protocol.ConvRoot, raw []byte, peer string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO conversations(id, root, kind, peer, pinned_at) VALUES(?, ?, ?, ?, ?)`,
		root.ID(), string(raw), root.Kind, peer, time.Now().Unix())
	return s.done(err)
}

func (s *store) conversations() ([]ConversationInfo, error) {
	rows, err := s.db.Query(`SELECT c.id, c.root, p.person, p.address, p.fingerprint, p.hash, p.roster, p.label, p.state
		FROM conversations c JOIN persons p ON p.person = c.peer ORDER BY c.pinned_at DESC, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConversationInfo
	for rows.Next() {
		var c ConversationInfo
		var raw, roster string
		var root protocol.ConvRoot
		if err := rows.Scan(&c.ID, &raw, &c.Peer.Person, &c.Peer.Address, &c.Peer.Fingerprint, &c.Peer.Roster, &roster, &c.Peer.Label, &c.Peer.State); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &root); err != nil {
			return nil, err
		}
		c.Kind, c.Created, c.Creator = root.Kind, root.Created, root.Creator.Address
		out = append(out, c)
	}
	return out, rows.Err()
}

// contentHash identifies what a conversation message says, independently
// of its per-device copy (envelope id, recipient, time, session): the same
// sender key and logical id must always carry the same content.
//
// The participation id is part of it (a message moved to another
// participation is not the same message). It is left out when empty, so
// the hash of a message without one is what it always was; legacyContentHash
// is the hash before it was added, for rows stored then (see addConvInbox).
func contentHash(in envelope.Inner) string {
	type att struct {
		Name   string
		Size   int64
		SHA256 string
	}
	c := struct {
		Conv, LID, Kind, Body, ReplyTo, Status, Sub, Origin, Emotion string
		Replica                                                      bool
		Target                                                       *envelope.Target
		Attachments                                                  []att
		PID                                                          string `json:",omitempty"`
	}{in.Conv, in.LID, in.Kind, in.Body, in.ReplyTo, in.Status, in.Sub, in.Origin, in.Emotion, in.Replica, in.Target, nil, in.PID}
	for _, a := range in.Attachments {
		c.Attachments = append(c.Attachments, att{a.Name, a.Size, a.SHA256})
	}
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func legacyContentHash(in envelope.Inner) string {
	in.PID = ""
	return contentHash(in)
}

// Results of addConvInbox.
const (
	admitted      = "admitted"
	admittedAgain = "duplicate"   // the same logical message again: nothing new
	admitConflict = "conflicting" // same key and logical id, different content
)

// addConvInbox admits a conversation message once per (verifying key,
// logical id). fromQuarantine removes the held envelope in the same step.
//
// also, if given, runs in the same transaction when the message is new
// (a participation event's own record): a duplicate or conflicting message
// has no effect at all, and if also fails nothing is stored.
func (s *store) addConvInbox(in envelope.Inner, verifiedBy, state string, fromQuarantine bool, also func(*sql.Tx) error) (string, error) {
	hash := contentHash(in)
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var stored, storedPID string
	err = tx.QueryRow(`SELECT coalesce(content_hash, ''), coalesce(pid, '') FROM inbox WHERE verified_by = ? AND lid = ?`, verifiedBy, in.LID).Scan(&stored, &storedPID)
	if err == nil && stored != hash && in.PID != "" && storedPID == in.PID && stored == legacyContentHash(in) {
		// Stored before the participation id was hashed: the same message.
		if _, err := tx.Exec(`UPDATE inbox SET content_hash = ? WHERE verified_by = ? AND lid = ?`, hash, verifiedBy, in.LID); err != nil {
			return "", err
		}
		stored = hash
	}
	switch {
	case err == nil && stored == hash:
		if fromQuarantine {
			if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, in.ID); err != nil {
				return "", err
			}
		}
		return admittedAgain, s.done(tx.Commit())
	case err == nil:
		return admitConflict, nil
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
	now := time.Now()
	res, err := tx.Exec(`INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, reply_to, received_at, session, status, state, verified_by,
		conv, lid, sub, replica, origin, emotion, target, content_hash, received_ms, pid)
		VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, nullif(?, ''))`,
		in.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, now.Unix(), in.Session, in.Status, state, verifiedBy,
		in.Conv, in.LID, in.Sub, in.Replica, in.Origin, in.Emotion, targetJSON(in.Target), hash, now.UnixMilli(), in.PID)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return admittedAgain, nil // this envelope id is stored already
	}
	if also != nil {
		if err := also(tx); err != nil {
			return "", err
		}
	}
	for _, a := range in.Attachments {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO attachments(message_id, blob_id, name, size, sha256, ct_size, ct_sha256) VALUES(?, ?, ?, ?, ?, ?, ?)`,
			in.ID, a.Blob.ID, a.Name, a.Size, a.SHA256, a.Blob.Size, a.Blob.SHA256); err != nil {
			return "", err
		}
	}
	if fromQuarantine {
		if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, in.ID); err != nil {
			return "", err
		}
	}
	return admitted, s.done(tx.Commit())
}

func targetJSON(t *envelope.Target) string {
	if t == nil {
		return ""
	}
	data, _ := json.Marshal(t)
	return string(data)
}

// convOf returns the conversation of a stored message, "" for a message
// outside any (version 1) or an unknown id.
func (s *store) convOf(id string) (string, error) {
	var conv string
	err := s.db.QueryRow(`SELECT coalesce(conv, '') FROM inbox WHERE id = ?
		UNION ALL SELECT coalesce(conv, '') FROM outbox WHERE id = ? LIMIT 1`, id, id).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return conv, err
}

// knownMessage reports whether id is a stored message.
func (s *store) knownMessage(id string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT (SELECT count(*) FROM inbox WHERE id = ?) + (SELECT count(*) FROM outbox WHERE id = ?)`, id, id).Scan(&n)
	return n > 0, err
}

// holdAs quarantines env with reason, or changes the reason it is held for.
func (s *store) holdAs(env envelope.Envelope, reason string) error {
	raw, _ := json.Marshal(env)
	_, err := s.db.Exec(`INSERT INTO quarantine(id, sender, reason, envelope, received_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason`, env.ID, env.From, reason, string(raw), time.Now().Unix())
	return s.done(err)
}

// heldPos is a position in the quarantine's (received_at, id) order.
type heldPos struct {
	at int64
	id string
}

// heldAfter returns up to limit envelopes held for reason after pos, in
// (received_at, id) order, and the position of the last one returned. The
// order is stable: re-holding a message changes its reason, not its place.
func (s *store) heldAfter(reason string, pos heldPos, limit int) ([]envelope.Envelope, heldPos, error) {
	rows, err := s.db.Query(`SELECT envelope, received_at, id FROM quarantine
		WHERE reason = ? AND (received_at > ? OR (received_at = ? AND id > ?)) ORDER BY received_at, id LIMIT ?`,
		reason, pos.at, pos.at, pos.id, limit)
	if err != nil {
		return nil, pos, err
	}
	defer rows.Close()
	var out []envelope.Envelope
	for rows.Next() {
		var data string
		var env envelope.Envelope
		if err := rows.Scan(&data, &pos.at, &pos.id); err != nil {
			return nil, pos, err
		}
		if err := json.Unmarshal([]byte(data), &env); err != nil {
			return nil, pos, err
		}
		out = append(out, env)
	}
	return out, pos, rows.Err()
}

// addConvOutbox records a sealed conversation message as queued (to send)
// or waiting (kept until the recipient can read it), with why, in one
// transaction with claim (if any), which decides that it may be stored.
// jobKey, if set, also records the message as a local request to this
// device's own agent (agentjob.go), verified by that key: one message in
// the conversation, one job, both or neither.
func (s *store) addConvOutbox(env envelope.Envelope, in envelope.Inner, state, why string, claim func(*sql.Tx, string) error, jobKey string) error {
	data, _ := json.Marshal(env)
	now := time.Now()
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
	if _, err := tx.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, error, created_at, reply_to, status, conv, lid, kind, origin, emotion, target, created_ms, pid, sub)
		VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, nullif(?, ''), nullif(?, ''))`,
		env.ID, env.To, in.Body, string(data), state, why, now.Unix(), in.ReplyTo, in.Status, in.Conv, in.LID,
		in.Kind, in.Origin, in.Emotion, targetJSON(in.Target), now.UnixMilli(), in.PID, in.Sub); err != nil {
		return err
	}
	if err := replyEndsReminder(tx, in.ReplyTo, in.Status); err != nil {
		return err
	}
	if jobKey != "" {
		// Read and acknowledged: it was never received, only asked here.
		if _, err := tx.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, reply_to, received_at, state, verified_by,
			conv, lid, origin, target, content_hash, received_ms, pid, local, acked, read_at)
			VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), ?, ?, ?, 1, 1, ?)`,
			env.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, now.Unix(), stateAgentWaiting, jobKey,
			in.Conv, in.LID, in.Origin, targetJSON(in.Target), contentHash(in), now.UnixMilli(), in.PID, now.Unix()); err != nil {
			return err
		}
	}
	return s.done(tx.Commit())
}

// convWaiting returns the ids and recipients of waiting conversation
// messages.
func (s *store) convWaiting() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT id, recipient FROM outbox WHERE state = ?`, stateConvWaiting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, to string
		if err := rows.Scan(&id, &to); err != nil {
			return nil, err
		}
		out[id] = to
	}
	return out, rows.Err()
}

// releaseWaiting queues a waiting conversation message for sending.
func (s *store) releaseWaiting(id string) error {
	_, err := s.db.Exec(`UPDATE outbox SET state = ?, error = NULL WHERE id = ? AND state = ?`, stateQueued, id, stateConvWaiting)
	return s.done(err)
}

// ConvMessage is one message of a conversation as this installation holds
// it, sent or received.
type ConvMessage struct {
	ID      string           `json:"id"`  // this copy's envelope id
	LID     string           `json:"lid"` // the logical id
	Dir     string           `json:"dir"` // in or out
	From    string           `json:"from"`
	Kind    string           `json:"kind"`
	Body    string           `json:"body"`
	ReplyTo string           `json:"reply_to,omitempty"`
	Sub     string           `json:"sub,omitempty"`
	Replica bool             `json:"replica,omitempty"`
	Origin  string           `json:"origin,omitempty"`  // the sender's assertion ("" for none)
	Emotion string           `json:"emotion,omitempty"` // "" means none sent
	Target  *envelope.Target `json:"target,omitempty"`
	PID     string           `json:"pid,omitempty"` // the agent participation it is for, from or about
	Key     string           `json:"key"`           // the sender's key fingerprint (received: the key that verified it)
	State   string           `json:"state"`         // inbox: its response state; outbox: queued, waiting, custody, delivered, not_delivered, …
	Detail  string           `json:"detail,omitempty"`
	At      int64            `json:"at"` // received or created here, unix seconds (listed in that order, to the millisecond)

	// A request this device's agent runs (received, or asked here by this
	// device's own person): the job's state and detail here (agentjob.go).
	Job       string `json:"job,omitempty"`
	JobDetail string `json:"job_detail,omitempty"`
}

// convMessages lists conv's messages; a local request to this device's own
// agent is listed once, as the message sent, with its job.
func (s *store) convMessages(conv, self, selfFP string) ([]ConvMessage, error) {
	rows, err := s.db.Query(`
		SELECT id, lid, 'in', sender, coalesce(verified_by, ''), kind, body, coalesce(reply_to, ''), coalesce(sub, ''), replica, coalesce(origin, ''),
		       coalesce(emotion, ''), coalesce(target, ''), state, coalesce(detail, ''), received_at, received_ms AS ms, coalesce(pid, ''),
		       CASE WHEN pid IS NOT NULL AND state != '' THEN state ELSE '' END, CASE WHEN pid IS NOT NULL AND state != '' THEN coalesce(detail, '') ELSE '' END
		  FROM inbox WHERE conv = ? AND local = 0
		UNION ALL
		SELECT o.id, o.lid, 'out', ?, ?, o.kind, o.body, coalesce(o.reply_to, ''), coalesce(o.sub, ''), 0, coalesce(o.origin, ''),
		       coalesce(o.emotion, ''), coalesce(o.target, ''), o.state, coalesce(o.error, ''), o.created_at, o.created_ms, coalesce(o.pid, ''),
		       coalesce(j.state, ''), coalesce(j.detail, '')
		  FROM outbox o LEFT JOIN inbox j ON j.id = o.id AND j.local = 1 WHERE o.conv = ?
		ORDER BY ms, 1`, conv, self, selfFP, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConvMessage
	for rows.Next() {
		var m ConvMessage
		var target string
		var ms int64
		if err := rows.Scan(&m.ID, &m.LID, &m.Dir, &m.From, &m.Key, &m.Kind, &m.Body, &m.ReplyTo, &m.Sub, &m.Replica, &m.Origin,
			&m.Emotion, &target, &m.State, &m.Detail, &m.At, &ms, &m.PID, &m.Job, &m.JobDetail); err != nil {
			return nil, err
		}
		if target != "" {
			m.Target = &envelope.Target{}
			json.Unmarshal([]byte(target), m.Target)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Participation events are append-only: each signed event is stored once,
// by hash, and never changed; a participation's state is resolved from
// them when read (participation.go).

// Bounds of the events held. They limit what a member could pile up, and
// never keep a participation from being stopped: a dismissal that follows
// an event of the participation held here is always stored, once per
// author key (a retry is the same event), whatever else is held; only
// dismissals following an event not held (yet) share a bound.
const (
	maxInvitesPerConversation  = 1000 // participations of one DM
	maxEventsPerParticipation  = 16   // its invite, decisions and events of other kinds
	maxDismissPerParticipation = 16   // its dismissals following an event not held (yet)
	maxPendingPerConversation  = 256  // events of participations whose invite is not held (yet)
)

var errTooManyEvents = errors.New("too many participation events")

// addParticipationEvent stores a verified event (the author's device key
// verified it), once.
func (s *store) addParticipationEvent(ev protocol.ParticipationEvent, raw []byte) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertParticipationEvent(tx, ev, raw); err != nil {
		return err
	}
	return s.done(tx.Commit())
}

// insertParticipationEvent stores ev within tx, once, within the bounds.
func insertParticipationEvent(tx *sql.Tx, ev protocol.ParticipationEvent, raw []byte) error {
	var exists, invited int
	if err := tx.QueryRow(`SELECT
		(SELECT count(*) FROM participation_events WHERE hash = ?),
		(SELECT count(*) FROM participation_events WHERE conv = ? AND type = ? AND pid = ?)`,
		ev.Hash(), ev.Conv, protocol.EventInvite, ev.PID).Scan(&exists, &invited); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	const knownPrev = `EXISTS (SELECT 1 FROM participation_events p WHERE p.conv = d.conv AND p.pid = d.pid AND p.hash = d.prev)`
	if ev.Type == protocol.EventDismiss {
		var known, stops, pendingStops int
		if err := tx.QueryRow(`SELECT
			(SELECT count(*) FROM participation_events WHERE conv = ? AND pid = ? AND hash = ?),
			(SELECT count(*) FROM participation_events d WHERE d.conv = ? AND d.pid = ? AND d.type = ? AND d.author = ? AND `+knownPrev+`),
			(SELECT count(*) FROM participation_events d WHERE d.conv = ? AND d.pid = ? AND d.type = ? AND NOT `+knownPrev+`)`,
			ev.Conv, ev.PID, ev.Prev,
			ev.Conv, ev.PID, protocol.EventDismiss, ev.Author.Fingerprint,
			ev.Conv, ev.PID, protocol.EventDismiss).Scan(&known, &stops, &pendingStops); err != nil {
			return err
		}
		switch {
		case known > 0 && stops == 0:
			return storeParticipationEvent(tx, ev, raw) // a stop following what is held: always room
		case known > 0:
			return errTooManyEvents // this key already stopped it
		case pendingStops >= maxDismissPerParticipation:
			return errTooManyEvents
		}
	} else {
		var ofKind int
		if err := tx.QueryRow(`SELECT count(*) FROM participation_events WHERE conv = ? AND pid = ? AND type != ?`,
			ev.Conv, ev.PID, protocol.EventDismiss).Scan(&ofKind); err != nil {
			return err
		}
		if ofKind >= maxEventsPerParticipation {
			return errTooManyEvents
		}
	}
	switch {
	case ev.Type == protocol.EventInvite:
		var invites int
		if err := tx.QueryRow(`SELECT count(*) FROM participation_events WHERE conv = ? AND type = ?`, ev.Conv, protocol.EventInvite).Scan(&invites); err != nil {
			return err
		}
		if invites >= maxInvitesPerConversation {
			return errTooManyEvents
		}
	case invited == 0: // it waits for an invite not held here
		var pending int
		if err := tx.QueryRow(`SELECT count(*) FROM participation_events e WHERE e.conv = ? AND e.type != ? AND NOT EXISTS
			(SELECT 1 FROM participation_events i WHERE i.conv = e.conv AND i.type = ? AND i.pid = e.pid)`,
			ev.Conv, protocol.EventInvite, protocol.EventInvite).Scan(&pending); err != nil {
			return err
		}
		if pending >= maxPendingPerConversation {
			return errTooManyEvents
		}
	}
	return storeParticipationEvent(tx, ev, raw)
}

func storeParticipationEvent(tx *sql.Tx, ev protocol.ParticipationEvent, raw []byte) error {
	_, err := tx.Exec(`INSERT INTO participation_events(hash, conv, pid, type, author, event, received_at, prev) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		ev.Hash(), ev.Conv, ev.PID, ev.Type, ev.Author.Fingerprint, string(raw), time.Now().Unix(), ev.Prev)
	return err
}

// participationEvents returns the events held for a participation.
func (s *store) participationEvents(conv, pid string) ([]protocol.ParticipationEvent, error) {
	return participationEventsIn(s.db, conv, pid)
}

func participationEventsIn(q dbq, conv, pid string) ([]protocol.ParticipationEvent, error) {
	rows, err := q.Query(`SELECT event FROM participation_events WHERE conv = ? AND pid = ? ORDER BY hash`, conv, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []protocol.ParticipationEvent
	for rows.Next() {
		var raw string
		var ev protocol.ParticipationEvent
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// participationIDs lists the participations with events in conv, and the
// conversation of one participation id.
func (s *store) participationIDs(conv string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT pid FROM participation_events WHERE conv = ? ORDER BY pid`, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err != nil {
			return nil, err
		}
		out = append(out, pid)
	}
	return out, rows.Err()
}

func (s *store) participationConv(pid string) (string, error) {
	var conv string
	err := s.db.QueryRow(`SELECT conv FROM participation_events WHERE pid = ? LIMIT 1`, pid).Scan(&conv)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoParticipation
	}
	return conv, err
}

// convLIDKeys returns the sender key fingerprints of the messages of conv
// held with logical id lid (received ones by the key that verified them,
// sent ones by selfFP).
func (s *store) convLIDKeys(conv, lid, selfFP string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT verified_by FROM inbox WHERE conv = ? AND lid = ?
		UNION SELECT ? FROM outbox WHERE conv = ? AND lid = ?`, conv, lid, selfFP, conv, lid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var fp string
		if err := rows.Scan(&fp); err != nil {
			return nil, err
		}
		out = append(out, fp)
	}
	return out, rows.Err()
}
