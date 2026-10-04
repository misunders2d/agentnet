package client

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Local state for human DMs: persons (this installation's own and pinned
// ones), conversation roots, and conversation messages in the inbox and
// outbox (schema step 13).

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

// ConversationInfo is one conversation this installation holds.
type ConversationInfo struct {
	ID      string       `json:"id"`
	Kind    string       `json:"kind"`
	Peer    PersonInfo   `json:"peer"`           // the other member
	Created int64        `json:"created"`        // the creator's claim, unix seconds
	Creator string       `json:"creator"`        // the creating device
	Role    string       `json:"role,omitempty"` // computed member or invited visitor; never authority
	Title   string       `json:"title,omitempty"`
	Members []PersonInfo `json:"members,omitempty"` // current group persons; never root membership substitution
	Frozen  string       `json:"frozen,omitempty"`  // current group eligibility prevents sending
	Deleted bool         `json:"deleted,omitempty"` // deleted here, nothing later to show (convclear.go): left out of lists
}

func (s *store) conversation(id string) (protocol.ConvRoot, []byte, bool, error) {
	return conversationIn(s.db, id)
}

func conversationIn(q querier, id string) (protocol.ConvRoot, []byte, bool, error) {
	var raw string
	err := q.QueryRow(`SELECT root FROM conversations WHERE id = ?`, id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		var packet GroupContext
		var data []byte
		err = q.QueryRow(`SELECT payload FROM group_context WHERE conv=?`, id).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			return protocol.ConvRoot{}, nil, false, nil
		}
		if err != nil {
			return protocol.ConvRoot{}, nil, false, err
		}
		if err = json.Unmarshal(data, &packet); err != nil {
			return protocol.ConvRoot{}, nil, false, err
		}
		root, _ := json.Marshal(packet.Root)
		return packet.Root, root, true, nil
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
	rows, err := s.db.Query(`SELECT id, root, peer FROM conversations ORDER BY pinned_at DESC, id`)
	if err != nil {
		return nil, err
	}
	type row struct{ id, raw, peer string }
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.raw, &r.peer); err != nil {
			rows.Close()
			return nil, err
		}
		rs = append(rs, r)
	}
	rows.Close()
	var out []ConversationInfo
	for _, r := range rs {
		var root protocol.ConvRoot
		if err := json.Unmarshal([]byte(r.raw), &root); err != nil {
			return nil, err
		}
		peer, ok, err := s.personByID(r.peer)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		out = append(out, ConversationInfo{ID: r.id, Kind: root.Kind, Peer: peer.info, Created: root.Created, Creator: root.Creator.Address})
	}
	return out, nil
}

// contentHash identifies what a conversation message says, independently
// of its per-device copy (envelope id, recipient, time, session, whether it
// is a replica or forwarded history, the blobs its files travel in): the
// same sender key and logical id must always carry the same content.
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
		Target                                                       *envelope.Target
		Attachments                                                  []att
		PID                                                          string                  `json:",omitempty"`
		AgentID                                                      string                  `json:",omitempty"`
		ReceiverRoute                                                *envelope.ReceiverRoute `json:",omitempty"`
		Human                                                        *envelope.HumanTurn     `json:",omitempty"`
	}{in.Conv, in.LID, in.Kind, in.Body, in.ReplyTo, in.Status, in.Sub, in.Origin, in.Emotion, in.Target, nil, in.PID, in.AgentID, in.ReceiverRoute, in.Human}
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

// refCols are the ref columns of a row: a control's target, else NULL.
func refCols(r *envelope.Ref) (any, any) {
	if r == nil {
		return nil, nil
	}
	return r.ID, r.Fingerprint
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
	// A copy forwarded as history of this message (same claimed key and
	// logical id) gives way to the one received directly: the direct copy
	// is stored in its place, under its own authority (history never did
	// any), keeping only whether it was read.
	var histID string
	var histRead sql.NullInt64
	switch err := tx.QueryRow(`SELECT id, read_at FROM inbox WHERE verified_by IS NULL AND claimed_fp = ? AND lid = ?`, verifiedBy, in.LID).Scan(&histID, &histRead); {
	case err == nil:
		if _, err := tx.Exec(`DELETE FROM attachments WHERE message_id = ?`, histID); err != nil {
			return "", err
		}
		if _, err := tx.Exec(`DELETE FROM inbox WHERE id = ?`, histID); err != nil {
			return "", err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return "", err
	}
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
	in = tombstoned(tx, in, verifiedBy) // deleted already (whatever order things arrive in): no text stored
	refID, refFP := refCols(in.Ref)
	res, err := tx.Exec(`INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, reply_to, received_at, session, status, state, verified_by,
		conv, lid, sub, replica, origin, emotion, target, content_hash, received_ms, pid, ref_id, ref_fp, agent_id, receiver_route)
		VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, nullif(?, ''), ?, ?, nullif(?, ''),nullif(?,''))`,
		in.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, now.Unix(), in.Session, in.Status, state, verifiedBy,
		in.Conv, in.LID, in.Sub, in.Replica, in.Origin, in.Emotion, targetJSON(in.Target), hash, now.UnixMilli(), in.PID, refID, refFP, in.AgentID, receiverRouteJSON(in.ReceiverRoute))
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return admittedAgain, nil // this envelope id is stored already
	}
	if in.Human != nil {
		if _, err := tx.Exec(`UPDATE inbox SET human=? WHERE id=?`, humanJSON(in.Human), in.ID); err != nil {
			return "", err
		}
	}
	if histRead.Valid {
		if _, err := tx.Exec(`UPDATE inbox SET read_at = ? WHERE id = ?`, histRead.Int64, in.ID); err != nil {
			return "", err
		}
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
	if err := bindReplyReceiverInput(tx, in, verifiedBy); err != nil {
		return "", err
	}
	if err := eraseArrivalIn(tx, in.ID); err != nil { // a turn deleted here keeps no text (convclear.go)
		return "", err
	}
	if fromQuarantine {
		if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, in.ID); err != nil {
			return "", err
		}
	}
	return admitted, s.done(tx.Commit())
}

// addHistoryInbox stores in, a message forwarded as history by via (a
// device of this installation's own person), as claimed sent under key
// claimedFP: never verified under that key here, never run, never an
// alert. A message already held under that key and logical id (directly or
// as history) is kept as it is. also runs with it when it is new.
// carrier is the id of the envelope that brought it (released from
// quarantine with it, if held).
// at (unix ms) places it in the conversation as the forwarding device had
// it (0: now).
func (s *store) addHistoryInbox(in envelope.Inner, at int64, claimedFP, via, carrier string, fromQuarantine bool, also func(*sql.Tx) error, guards ...func(*sql.Tx) error) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	for _, guard := range guards {
		if err := guard(tx); err != nil {
			return "", err
		}
	}
	if err := receiverHistoryRoute(tx, in, claimedFP); err != nil {
		return "", err
	}
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM inbox WHERE coalesce(verified_by, claimed_fp) = ? AND lid = ?`, claimedFP, in.LID).Scan(&n); err != nil {
		return "", err
	}
	result := admittedAgain
	if n == 0 {
		if at <= 0 || at > time.Now().UnixMilli() {
			at = time.Now().UnixMilli()
		}
		in = tombstoned(tx, in, claimedFP) // history of a deleted message carries no text
		refID, refFP := refCols(in.Ref)
		res, err := tx.Exec(`INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, reply_to, received_at, status, state,
			conv, lid, sub, replica, origin, emotion, target, content_hash, received_ms, pid, claimed_fp, via, acked, ref_id, ref_fp, agent_id)
			VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), '', ?, ?, nullif(?, ''), 1, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, nullif(?, ''), ?, ?, 1, ?, ?, nullif(?, ''))`,
			in.ID, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, in.TS, in.Status,
			in.Conv, in.LID, in.Sub, in.Origin, in.Emotion, targetJSON(in.Target), contentHash(in), at, in.PID, claimedFP, via, refID, refFP, in.AgentID)
		if err != nil {
			return "", err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			result = admitted
			if in.Human != nil {
				if _, err := tx.Exec(`UPDATE inbox SET human=? WHERE id=?`, humanJSON(in.Human), in.ID); err != nil {
					return "", err
				}
			}
			if in.ReceiverRoute != nil {
				if _, err = tx.Exec(`UPDATE inbox SET receiver_route=? WHERE id=? AND claimed_fp=? AND conv=?`, receiverRouteJSON(in.ReceiverRoute), in.ID, claimedFP, in.Conv); err != nil {
					return "", err
				}
			}
			if also != nil {
				if err := also(tx); err != nil {
					return "", err
				}
			}
			for i, a := range in.Attachments { // the manifest only: the file comes on request (files.go)
				if _, err := tx.Exec(`INSERT OR IGNORE INTO attachments(message_id, blob_id, name, size, sha256, ct_size, ct_sha256) VALUES(?, ?, ?, ?, ?, 0, '')`,
					in.ID, fmt.Sprintf("%s%d", historyBlob, i), a.Name, a.Size, a.SHA256); err != nil {
					return "", err
				}
			}
			if err := eraseArrivalIn(tx, in.ID); err != nil { // history of a turn deleted here keeps no text (convclear.go)
				return "", err
			}
		}
	}
	if fromQuarantine {
		if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, carrier); err != nil {
			return "", err
		}
	}
	return result, s.done(tx.Commit())
}

// historyBlob starts the placeholder blob id of a file known from history
// only (its manifest): no ciphertext for this device exists yet.
const historyBlob = "history-"

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
func (s *store) addConvOutbox(copies []outCopy, local envelope.Inner, claim func(*sql.Tx, string) error, jobKey string, selected ...*replyBinding) error {
	originals := copies
	if len(selected) > 0 && selected[0] != nil {
		originals = nil
		for _, c := range copies {
			if c.in.Sub == "" {
				originals = append(originals, c)
			}
		}
	}
	if len(selected) > 0 && selected[0] != nil && selected[0].setup != nil {
		copies = append(append([]outCopy(nil), copies...), *selected[0].setup)
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	first := local.ID
	if len(copies) > 0 {
		first = copies[0].env.ID
	}
	if claim != nil {
		if err := claim(tx, first); err != nil {
			return err
		}
	}
	for i, c := range copies {
		env, in := c.env, c.in
		data, _ := json.Marshal(env)
		refID, refFP := refCols(in.Ref)
		if _, err := tx.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, error, created_at, reply_to, status, conv, lid, kind, origin, emotion, target, created_ms, pid, sub, ref_id, ref_fp, agent_id, required_cap, recipient_fp,group_admission)
			VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''), ?, nullif(?, ''), nullif(?, ''), ?, ?, nullif(?, ''), nullif(?, ''), nullif(?, ''),nullif(?,''))`,
			env.ID, env.To, in.Body, string(data), c.state, c.why, now.Unix(), in.ReplyTo, in.Status, in.Conv, in.LID,
			in.Kind, in.Origin, in.Emotion, targetJSON(in.Target), now.UnixMilli(), in.PID, in.Sub, refID, refFP, in.AgentID, copyRequirement(c), c.recipientFP, c.groupAdmission); err != nil {
			return err
		}
		if i == 0 {
			if err := replyEndsReminder(tx, in.ReplyTo, in.Status); err != nil {
				return err
			}
			if err := turnClosesHeld(tx, in, now.UnixMilli()); err != nil {
				return err
			}
		}
		if in.Human != nil {
			if _, err := tx.Exec(`UPDATE outbox SET human=? WHERE id=?`, humanJSON(in.Human), env.ID); err != nil {
				return err
			}
		}
		for _, a := range in.Attachments { // as addOutbox: what was sent, and what must be uploaded first
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
	}
	if len(selected) > 0 {
		if err := bindReplyReceiver(tx, selected[0], originals); err != nil {
			return err
		}
	}
	if jobKey != "" {
		in := local
		if len(copies) > 0 {
			in = copies[0].in
		}
		// Read and acknowledged: it was never received, only asked here.
		if _, err := tx.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, reply_to, received_at, state, verified_by,
			conv, lid, origin, target, content_hash, received_ms, pid, local, acked, read_at)
			VALUES(?, ?, ?, ?, ?, nullif(?, ''), ?, ?, ?, ?, ?, nullif(?, ''), nullif(?, ''), ?, ?, ?, 1, 1, ?)`,
			first, in.From, in.TS, in.Kind, in.Body, in.ReplyTo, now.Unix(), stateAgentWaiting, jobKey,
			in.Conv, in.LID, in.Origin, targetJSON(in.Target), contentHash(in), now.UnixMilli(), in.PID, now.Unix()); err != nil {
			return err
		}
	}
	return s.done(tx.Commit())
}

// turnClosesHeld closes the questions and tasks held for the person
// (stateConvHeld: nothing runs them) in the conversation of turn in, a turn
// the person sent, here or from another of their devices: they answered
// there. Only what had reached this device when the turn was written
// closes: written is that time in Unix milliseconds (now, for a turn sent
// here; the start of the second its device stamped, in.TS, for one from
// another device), so a turn written earlier and delivered late answers
// nothing that came after it. The turn's own copy is left as it is; a
// request to an agent, an agent's output, a control or a record closes
// nothing.
func turnClosesHeld(tx *sql.Tx, in envelope.Inner, written int64) error {
	if in.Conv == "" || in.Sub != "" || in.Target != nil || strings.HasPrefix(in.Origin, envelope.OriginAgentPrefix) {
		return nil
	}
	_, err := tx.Exec(`UPDATE inbox SET state = ?, detail = ? WHERE conv = ? AND state = ? AND id != ? AND coalesce(received_ms, received_at * 1000) <= ?`,
		stateManual, "answered in the conversation", in.Conv, stateConvHeld, in.ID, written)
	return err
}

// waitingCopy is a waiting conversation copy: its recipient and sub (a
// dedicated record needs its own capability before release).
type waitingCopy struct {
	to, sub, required, status, agentID, conv, pid, body string
	human                                               bool   // carries a captured audience (hgp1 besides its requirement)
	humanRaw                                            string // that audience, as stored (rm1 besides it for a room's: roomCopy)
}

// convWaiting returns the ids and recipients of waiting conversation
// messages.
func (s *store) convWaiting() (map[string]waitingCopy, error) {
	rows, err := s.db.Query(`SELECT id, recipient, coalesce(sub, ''), coalesce(required_cap, ''), coalesce(status, ''), coalesce(agent_id, ''), coalesce(conv, ''), coalesce(pid, ''), coalesce(body, ''), coalesce(human, '') FROM outbox WHERE state = ?`, stateConvWaiting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]waitingCopy{}
	for rows.Next() {
		var id string
		var w waitingCopy
		if err := rows.Scan(&id, &w.to, &w.sub, &w.required, &w.status, &w.agentID, &w.conv, &w.pid, &w.body, &w.humanRaw); err != nil {
			return nil, err
		}
		w.human = w.humanRaw != ""
		out[id] = w
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
	Human      *envelope.HumanTurn `json:"human,omitempty"`
	ExcerptPID string              `json:"excerpt_pid,omitempty"` // scope of a claimed snapshot; original PID stays PID
	ID         string              `json:"id"`                    // this copy's envelope id
	LID        string              `json:"lid"`                   // the logical id
	Dir        string              `json:"dir"`                   // in or out
	From       string              `json:"from"`
	Kind       string              `json:"kind"`
	Body       string              `json:"body"`
	ReplyTo    string              `json:"reply_to,omitempty"`
	Sub        string              `json:"sub,omitempty"`
	Replica    bool                `json:"replica,omitempty"`
	Origin     string              `json:"origin,omitempty"`  // the sender's assertion ("" for none)
	Emotion    string              `json:"emotion,omitempty"` // "" means none sent
	Target     *envelope.Target    `json:"target,omitempty"`
	PID        string              `json:"pid,omitempty"`         // the agent participation it is for, from or about
	AgentID    string              `json:"agent_id,omitempty"`    // the named answer/result author, asserted by its host
	Key        string              `json:"key"`                   // the sender's key fingerprint (received: the key that verified it)
	Claimed    string              `json:"claimed_key,omitempty"` // history only: the key the forwarding device says sent it (Key is then empty)
	State      string              `json:"state"`                 // inbox: its response state; outbox: queued, waiting, custody, delivered, not_delivered, …
	Detail     string              `json:"detail,omitempty"`
	At         int64               `json:"at"` // received or created here, unix seconds (listed in that order, to the millisecond)

	// An agent's turn (agentTurn) sent by its participation's exact host
	// key, as admission checks it (checkConversationAgent): for history,
	// the original key its own device vouched for. Never inferred from
	// Origin or AgentID; a claimed excerpt is never one. It speaks for the
	// turn as sent (Body): an edit, which any device of the host's person
	// may make, shows in Controls and is not the host key's. status is the
	// turn's own (an agent's progress is a message).
	VerifiedAgent bool `json:"verified_agent"`
	status        string

	// A request this device's agent runs (received, or asked here by this
	// device's own person): the job's state and detail here (agentjob.go).
	Job       string `json:"job,omitempty"`
	JobDetail string `json:"job_detail,omitempty"`

	// Its files: received ones with where they were saved, sent ones as
	// sent (the sender keeps no plaintext to open).
	Attachments []FileInfo `json:"attachments,omitempty"`

	// Sent by this person: Via names the device it was sent from when that
	// is not this one; Copies are the copies this device sent, one per
	// device (State is then the least advanced one's).
	Via    string     `json:"via,omitempty"`
	Copies []ConvCopy `json:"copies,omitempty"`

	// History: forwarded by SyncedFrom, a device of this person; its
	// sender and key (Key is then empty) are that device's word, not
	// verified here, and it never runs anything.
	History    bool      `json:"history,omitempty"`
	SyncedFrom string    `json:"synced_from,omitempty"`
	Controls             // reactions, edits and deletion applied to it (controls.go)
	Exec       *ExecView `json:"exec,omitempty"` // a request: where its executor last said it stands (headless.go)
}

// convMessages lists conv's messages; a local request to this device's own
// agent is listed once, as the message sent, with its job.
// own names the devices of this installation's person (any step of its
// chain): received messages from them are this person's own, sent from
// another device.
func (s *store) convMessages(conv, self, selfFP string, own map[string]bool) ([]ConvMessage, error) {
	// A received request's agent_id is its local executor stamp. The
	// request remains its human sender's turn; only replies name an author.
	rows, err := s.db.Query(`
		SELECT id, lid, 'in', sender, coalesce(verified_by, ''), kind, body, coalesce(reply_to, ''), coalesce(sub, ''), replica, coalesce(origin, ''),
		       coalesce(emotion, ''), coalesce(target, ''), state, coalesce(detail, ''), received_at, received_ms AS ms, coalesce(pid, ''),
		       CASE WHEN pid IS NOT NULL AND state != '' THEN state ELSE '' END, CASE WHEN pid IS NOT NULL AND state != '' THEN coalesce(detail, '') ELSE '' END, coalesce(via, ''),
		       CASE WHEN verified_by IS NULL THEN coalesce(claimed_fp, '') ELSE '' END,
		       CASE WHEN kind IN ('question', 'task') THEN '' ELSE coalesce(agent_id, '') END, coalesce(status, '')
		  FROM inbox i WHERE conv = ? AND local = 0 AND ref_id IS NULL AND coalesce(sub, '') NOT IN `+recordSubs+`
		   AND NOT `+erasedIn+`
		UNION ALL
		SELECT o.id, o.lid, 'out', ?, ?, o.kind, o.body, coalesce(o.reply_to, ''), coalesce(o.sub, ''), 0, coalesce(o.origin, ''),
		       coalesce(o.emotion, ''), coalesce(o.target, ''), o.state, coalesce(o.error, ''), o.created_at, o.created_ms, coalesce(o.pid, ''),
		       coalesce(j.state, ''), coalesce(j.detail, ''), o.recipient, '', coalesce(o.agent_id, ''), coalesce(o.status, '')
		  FROM outbox o LEFT JOIN inbox j ON j.id = o.id AND j.local = 1 WHERE o.conv = ? AND coalesce(o.sub, '') NOT IN ('history', 'file', 'drive-space', 'group-proof', 'group-context','group-invite','group-consent','group-withdrawal') AND o.ref_id IS NULL
		   AND NOT `+erasedOut+`
		ORDER BY ms, 1`, conv, self, selfFP, conv, selfFP)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConvMessage
	sent := map[string]int{} // logical id of a message sent here: its index in out
	for rows.Next() {
		var m ConvMessage
		var target, to string
		var ms int64
		if err := rows.Scan(&m.ID, &m.LID, &m.Dir, &m.From, &m.Key, &m.Kind, &m.Body, &m.ReplyTo, &m.Sub, &m.Replica, &m.Origin,
			&m.Emotion, &target, &m.State, &m.Detail, &m.At, &ms, &m.PID, &m.Job, &m.JobDetail, &to, &m.Claimed, &m.AgentID, &m.status); err != nil {
			return nil, err
		}
		if target != "" {
			m.Target = &envelope.Target{}
			json.Unmarshal([]byte(target), m.Target)
		}
		if m.Dir == "in" && to != "" { // history: forwarded by another device of this person
			m.History, m.SyncedFrom, to = true, to, ""
		}
		switch {
		case m.Dir == "out":
			c := ConvCopy{ID: m.ID, To: to, State: m.State, Detail: m.Detail, Own: own[to]}
			if i, ok := sent[m.LID]; ok {
				out[i].Copies = append(out[i].Copies, c)
				if rank(c.State) < rank(out[i].State) {
					out[i].State, out[i].Detail = c.State, c.Detail
				}
				// Its id is a copy to someone else when there is one, so a
				// status of the id shown is about them, never about one of
				// this person's own devices.
				if !c.Own && out[i].Copies[0].Own && out[i].ID == out[i].Copies[0].ID {
					out[i].ID = c.ID
				}
				continue
			}
			m.Copies = []ConvCopy{c}
			sent[m.LID] = len(out)
		case own[m.From]:
			m.Dir, m.Via = "out", m.From
		}
		out = append(out, m)
	}
	fileKey := func(m ConvMessage) string {
		if m.Via != "" {
			return "in/" + m.ID // received from another device of this person
		}
		return m.Dir + "/" + m.ID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	files, err := s.convFiles(conv)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Attachments = files[fileKey(out[i])]
	}
	return out, nil
}

// convFiles returns the files of conv's messages by "in/ID" and "out/ID".
func (s *store) convFiles(conv string) (map[string][]FileInfo, error) {
	out := map[string][]FileInfo{}
	rows, err := s.db.Query(`
		SELECT 'in/' || a.message_id, a.blob_id, a.name, a.size, a.sha256, a.ct_size, a.ct_sha256, coalesce(a.saved_path, ''), a.rowid, 0
		  FROM attachments a JOIN inbox i ON i.id = a.message_id WHERE i.conv = ? AND i.local = 0
		UNION ALL
		SELECT 'out/' || f.message_id, f.blob_id, f.name, f.size, f.sha256, 0, '', '', f.rowid, 1
		  FROM sent_attachments f JOIN outbox o ON o.id = f.message_id WHERE o.conv = ?
		ORDER BY 10, 9`, conv, conv)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var f FileInfo
		var rowid, sent int64
		if err := rows.Scan(&key, &f.BlobID, &f.Name, &f.Size, &f.SHA256, &f.ctSize, &f.ctSHA256, &f.SavedPath, &rowid, &sent); err != nil {
			return nil, err
		}
		out[key] = append(out[key], f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for key, files := range out { // known from history only: where its request stands
		for i, f := range files {
			if strings.HasPrefix(f.BlobID, historyBlob) {
				files[i].Availability = "requestable"
				var state string
				if s.db.QueryRow(`SELECT state FROM file_requests WHERE message_id = ? AND sha256 = ?`, strings.TrimPrefix(key, "in/"), f.SHA256).Scan(&state) == nil {
					files[i].Availability = state
				}
			}
		}
	}
	return out, nil
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
	return s.addParticipationEventWith(ev, raw, nil)
}

// addParticipationEventWith stores ev as addParticipationEvent does, in one
// transaction with also, which runs first: its error stores nothing.
func (s *store) addParticipationEventWith(ev protocol.ParticipationEvent, raw []byte, also func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if also != nil {
		if err := also(tx); err != nil {
			return err
		}
	}
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
