package client

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
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
}

func scanPerson(row interface{ Scan(...any) error }) (personRow, bool, error) {
	var p personRow
	var raw string
	err := row.Scan(&p.info.Person, &p.info.Address, &p.info.Fingerprint, &p.info.Roster, &raw, &p.info.Label, &p.info.State)
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
	return p, true, nil
}

const personCols = `person, address, fingerprint, hash, roster, label, state`

func (s *store) selfPerson() (personRow, bool, error) {
	return scanPerson(s.db.QueryRow(`SELECT `+personCols+` FROM persons WHERE state = ?`, personSelf))
}

func (s *store) personByID(id string) (personRow, bool, error) {
	return scanPerson(s.db.QueryRow(`SELECT `+personCols+` FROM persons WHERE person = ?`, id))
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

// pinPerson pins a verified roster. A different record for a pinned person
// id, or another person for a pinned address, freezes the pinned one as a
// conflict (errPersonConflict); nothing is replaced.
func (s *store) pinPerson(r protocol.PersonRoster, raw []byte) error {
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
	if _, err := tx.Exec(`INSERT INTO persons(person, address, fingerprint, hash, roster, label, state, pinned_at) VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Person, d.Address, d.Fingerprint, r.Hash(), string(raw), r.Label, personPinned, time.Now().Unix()); err != nil {
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
	var raw string
	err := s.db.QueryRow(`SELECT root FROM conversations WHERE id = ?`, id).Scan(&raw)
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
	}{in.Conv, in.LID, in.Kind, in.Body, in.ReplyTo, in.Status, in.Sub, in.Origin, in.Emotion, in.Replica, in.Target, nil}
	for _, a := range in.Attachments {
		c.Attachments = append(c.Attachments, att{a.Name, a.Size, a.SHA256})
	}
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Results of addConvInbox.
const (
	admitted      = "admitted"
	admittedAgain = "duplicate"   // the same logical message again: nothing new
	admitConflict = "conflicting" // same key and logical id, different content
)

// addConvInbox admits a conversation message once per (verifying key,
// logical id). fromQuarantine removes the held envelope in the same step.
func (s *store) addConvInbox(in envelope.Inner, verifiedBy, state string, fromQuarantine bool) (string, error) {
	hash := contentHash(in)
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var stored string
	err = tx.QueryRow(`SELECT coalesce(content_hash, '') FROM inbox WHERE verified_by = ? AND lid = ?`, verifiedBy, in.LID).Scan(&stored)
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
		conv, lid, sub, replica, origin, emotion, target, content_hash, received_ms)
		VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?)`,
		in.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, now.Unix(), in.Session, in.Status, state, verifiedBy,
		in.Conv, in.LID, in.Sub, in.Replica, in.Origin, in.Emotion, targetJSON(in.Target), hash, now.UnixMilli())
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return admittedAgain, nil // this envelope id is stored already
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

// holdAs quarantines env with reason, or changes the reason it is held for.
func (s *store) holdAs(env envelope.Envelope, reason string) error {
	raw, _ := json.Marshal(env)
	_, err := s.db.Exec(`INSERT INTO quarantine(id, sender, reason, envelope, received_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET reason = excluded.reason`, env.ID, env.From, reason, string(raw), time.Now().Unix())
	return s.done(err)
}

// heldFor returns up to limit envelopes held for reason, oldest first.
func (s *store) heldFor(reason string, limit int) ([]envelope.Envelope, error) {
	rows, err := s.db.Query(`SELECT envelope FROM quarantine WHERE reason = ? ORDER BY received_at, id LIMIT ?`, reason, limit)
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

// addConvOutbox records a sealed conversation message as queued (to send)
// or waiting (kept until the recipient can read it), with why.
func (s *store) addConvOutbox(env envelope.Envelope, in envelope.Inner, state, why string) error {
	data, _ := json.Marshal(env)
	now := time.Now()
	_, err := s.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, error, created_at, reply_to, status, conv, lid, kind, origin, emotion, target, created_ms)
		VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?)`,
		env.ID, env.To, in.Body, string(data), state, why, now.Unix(), in.ReplyTo, in.Status, in.Conv, in.LID,
		in.Kind, in.Origin, in.Emotion, targetJSON(in.Target), now.UnixMilli())
	return s.done(err)
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
	State   string           `json:"state"` // inbox: its response state; outbox: queued, waiting, custody, delivered, …
	Detail  string           `json:"detail,omitempty"`
	At      int64            `json:"at"` // received or created here, unix seconds (listed in that order, to the millisecond)
}

func (s *store) convMessages(conv, self string) ([]ConvMessage, error) {
	rows, err := s.db.Query(`
		SELECT id, lid, 'in', sender, kind, body, coalesce(reply_to, ''), coalesce(sub, ''), replica, coalesce(origin, ''),
		       coalesce(emotion, ''), coalesce(target, ''), state, coalesce(detail, ''), received_at, received_ms AS ms
		  FROM inbox WHERE conv = ?
		UNION ALL
		SELECT id, lid, 'out', ?, kind, body, coalesce(reply_to, ''), '', 0, coalesce(origin, ''),
		       coalesce(emotion, ''), coalesce(target, ''), state, coalesce(error, ''), created_at, created_ms
		  FROM outbox WHERE conv = ?
		ORDER BY ms, 1`, conv, self, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConvMessage
	for rows.Next() {
		var m ConvMessage
		var target string
		var ms int64
		if err := rows.Scan(&m.ID, &m.LID, &m.Dir, &m.From, &m.Kind, &m.Body, &m.ReplyTo, &m.Sub, &m.Replica, &m.Origin,
			&m.Emotion, &target, &m.State, &m.Detail, &m.At, &ms); err != nil {
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
