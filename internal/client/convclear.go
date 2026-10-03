package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Deleting a conversation (owner decision 2026-10-03: from all of the
// person's own devices). The person's copy of one conversation is erased on
// every device of that person; the other people in it keep theirs, and
// nothing about membership, guests, approvals or running work changes.
//
// What is erased is named exactly: every turn (logical id and sender key)
// of the conversation the deleting device holds. The names travel as a
// version 3 control (sub "clear") to the person's other devices only, held
// for each until it reads clr1 (protocol.CapConvClear); a device linked
// later gets every name erased here (replayErased), and one the deleting
// device did not know of gets them from a device that did (forwardClear).
// Each device keeps every erased name (conv_erased), so a copy of an erased
// turn that arrives later, by retry, restart, another device's history or a
// late delivery, is kept only as a skeleton: its ids still stop duplicates,
// nothing shows it.
//
// Nothing else is inferred: a turn the deleting device never held is not
// named, so a device that holds it keeps it (and shows it), as any later
// turn. No order or time decides what is erased.
//
// A body that unfinished work still needs (a request the worker or a person
// still has to deal with, a reply a selected receiver has not taken, a copy
// not yet sent) is hidden at once and erased by the local change that ends
// that work (eraseLoop, on the daemon's change feed, as the worker's).
//
// A device thread (no conversation) exists on this device only: deleting
// one erases exactly that reply-linked thread here, and tells no one.
const convClearSchema = `
CREATE TABLE conv_erased(
  conv TEXT NOT NULL,
  key TEXT NOT NULL,
  lid TEXT NOT NULL,
  deletion TEXT NOT NULL,
  shared INTEGER NOT NULL DEFAULT 1,
  PRIMARY KEY(conv, key, lid));
CREATE INDEX conv_erased_unshared ON conv_erased(deletion) WHERE shared = 0;
`

// erasableSubs are the rows whose text a deletion removes: turns, shared
// excerpts and message controls. Membership, proof, status, decision, file,
// history, drive and deletion rows stay as they are.
const erasableSubs = `('', 'excerpt', 'reaction', 'revision', 'retraction')`

// erasedIn matches inbox rows (alias i) whose turn is erased here;
// erasedInFor names the inbox table or alias t.
var erasedIn = erasedInFor("i")

func erasedInFor(t string) string {
	return `EXISTS (SELECT 1 FROM conv_erased e WHERE e.conv = coalesce(` + t + `.conv, '') AND e.key = coalesce(` + t + `.verified_by, ` + t + `.claimed_fp, '')
	AND e.lid = CASE WHEN ` + t + `.conv IS NULL THEN ` + t + `.id ELSE ` + t + `.lid END)`
}

// erasedOut matches outbox rows (alias o) whose turn is erased here; its one
// argument is this device's own key.
const erasedOut = `EXISTS (SELECT 1 FROM conv_erased e WHERE e.conv = coalesce(o.conv, '') AND e.key = ?
	AND e.lid = CASE WHEN o.conv IS NULL THEN o.id ELSE o.lid END)`

// retainedIn matches inbox rows (alias i) whose text unfinished work needs:
// a request the worker may still run or a person may still accept, decline
// or resolve, and a reply a selected receiver has not taken. A question or
// task for the person in a DM (conv_held) never runs: it is not kept.
const retainedIn = `(i.kind IN ('question', 'task') AND i.state IN ('pending', 'accepted', 'held', 'awaiting', 'running', 'cancel_requested', 'part_waiting', 'needs_human')
	OR EXISTS (SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id = i.id AND x.state = 'pending'))`

// retainedOut matches outbox rows (alias o) not yet handed over.
const retainedOut = `o.state IN ('queued', 'waiting')`

// ConversationDeleted is what a deletion did here.
type ConversationDeleted struct {
	Deletion string `json:"deletion"`
	Erased   int    `json:"erased"`              // turns erased here
	Kept     int    `json:"kept,omitempty"`      // of those, hidden but kept until their work ends
	Devices  int    `json:"devices,omitempty"`   // other devices of this person it is queued for (each applies it when it can read it)
	ThisOnly bool   `json:"this_only,omitempty"` // a device thread: it exists on this device only
}

// ErrNothingToDelete refuses a deletion that would erase nothing.
var ErrNothingToDelete = errors.New("nothing here to delete")

// ErrNoConversation means no conversation with that id is held here.
var ErrNoConversation = errors.New("no such conversation here")

// convTurns names the erasable turns of conv held here and not erased yet.
func convTurns(q dbq, conv, selfFP string) ([]envelope.Ref, error) {
	rows, err := q.Query(`
		SELECT coalesce(i.verified_by, i.claimed_fp), i.lid FROM inbox i
		 WHERE i.conv = ? AND i.local = 0 AND i.lid IS NOT NULL AND coalesce(i.verified_by, i.claimed_fp) IS NOT NULL AND coalesce(i.sub, '') IN `+erasableSubs+` AND NOT `+erasedIn+`
		UNION
		SELECT ?, o.lid FROM outbox o
		 WHERE o.conv = ? AND o.lid IS NOT NULL AND coalesce(o.sub, '') IN `+erasableSubs+` AND NOT `+erasedOut+`
		ORDER BY 1, 2`, conv, selfFP, conv, selfFP)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []envelope.Ref
	for rows.Next() {
		var r envelope.Ref
		if err := rows.Scan(&r.Fingerprint, &r.ID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeleteConversation erases this person's copy of conversation conv here:
// every turn of it held here, by name, and queues those names for this
// person's other devices.
func (a *Agent) DeleteConversation(ctx context.Context, conv string) (ConversationDeleted, error) {
	if _, _, found, err := a.store.conversation(conv); err != nil {
		return ConversationDeleted{}, err
	} else if !found {
		return ConversationDeleted{}, ErrNoConversation
	}
	selfFP := a.Self().Fingerprint()
	out := ConversationDeleted{Deletion: protocol.NewID()}
	tx, err := a.store.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	turns, err := convTurns(tx, conv, selfFP)
	if err != nil {
		return out, err
	}
	if len(turns) == 0 {
		return out, ErrNothingToDelete
	}
	for _, t := range turns {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO conv_erased(conv, key, lid, deletion, shared) VALUES(?, ?, ?, ?, 0)`, conv, t.Fingerprint, t.ID, out.Deletion); err != nil {
			return out, err
		}
	}
	if err := eraseCoveredIn(tx, conv, selfFP); err != nil {
		return out, err
	}
	out.Erased = len(turns)
	if out.Kept, err = retainedCount(tx, conv, selfFP); err != nil {
		return out, err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return out, err
	}
	a.dropErasedFiles(conv)
	out.Devices, err = a.shareErased(ctx)
	notifyDaemon(a.home)
	return out, err
}

// DeleteThread erases exactly the device thread with peer whose earliest
// message is id (ThreadSummary.ID), here: device threads are kept by this
// device only. Other threads with the same peer stay.
func (a *Agent) DeleteThread(peer, id string) (ConversationDeleted, error) {
	groups, rows, err := a.peerThreadGroups(peer)
	if err != nil {
		return ConversationDeleted{}, err
	}
	var thread []string
	for _, g := range groups {
		if len(g) > 0 && g[0] == id {
			thread = g
		}
	}
	if len(thread) == 0 {
		return ConversationDeleted{}, ErrNoMessage
	}
	selfFP := a.Self().Fingerprint()
	out := ConversationDeleted{Deletion: protocol.NewID(), ThisOnly: true}
	tx, err := a.store.db.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	for _, m := range thread {
		key := selfFP
		if rows[m].in {
			if err := tx.QueryRow(`SELECT verified_by FROM inbox WHERE id = ? AND conv IS NULL`, m).Scan(&key); err != nil {
				return out, err
			}
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO conv_erased(conv, key, lid, deletion) VALUES('', ?, ?, ?)`, key, m, out.Deletion); err != nil {
			return out, err
		}
		out.Erased++
	}
	// Controls on its messages are erased with them.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO conv_erased(conv, key, lid, deletion)
		SELECT '', i.verified_by, i.id, ? FROM inbox i JOIN conv_erased t ON t.conv = '' AND t.lid = i.ref_id AND t.key = i.ref_fp
		 WHERE i.conv IS NULL AND i.ref_id IS NOT NULL AND i.verified_by IS NOT NULL AND t.deletion = ?
		UNION SELECT '', ?, o.id, ? FROM outbox o JOIN conv_erased t ON t.conv = '' AND t.lid = o.ref_id AND t.key = o.ref_fp
		 WHERE o.conv IS NULL AND o.ref_id IS NOT NULL AND t.deletion = ?`, out.Deletion, out.Deletion, selfFP, out.Deletion, out.Deletion); err != nil {
		return out, err
	}
	if err := eraseCoveredIn(tx, "", selfFP); err != nil {
		return out, err
	}
	if out.Kept, err = retainedCount(tx, "", selfFP); err != nil {
		return out, err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return out, err
	}
	a.dropErasedFiles("")
	notifyDaemon(a.home)
	return out, nil
}

// eraseCoveredIn blanks what erased turns of conv ("" for device threads)
// still say here, except what unfinished work needs (retainedIn/Out). Ids,
// states and receipts stay: they stop duplicates and keep authority.
func eraseCoveredIn(tx *sql.Tx, conv, selfFP string) error {
	if _, err := tx.Exec(`UPDATE inbox SET body = '' WHERE id IN (SELECT i.id FROM inbox i WHERE coalesce(i.conv, '') = ? AND i.local = 0
		AND coalesce(i.sub, '') IN `+erasableSubs+` AND i.body <> '' AND `+erasedIn+` AND NOT `+retainedIn+`)`, conv); err != nil {
		return err
	}
	// This device's own requests to its own agent (convstore: local rows, the
	// job of a sent turn) follow the sent turn.
	if _, err := tx.Exec(`UPDATE inbox SET body = '' WHERE id IN (SELECT i.id FROM inbox i JOIN outbox o ON o.id = i.id WHERE i.local = 1 AND coalesce(o.conv, '') = ?
		AND i.body <> '' AND `+erasedOut+` AND NOT `+retainedIn+`)`, conv, selfFP); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE outbox SET body = '' WHERE id IN (SELECT o.id FROM outbox o WHERE coalesce(o.conv, '') = ?
		AND coalesce(o.sub, '') IN `+erasableSubs+` AND o.body <> '' AND `+erasedOut+` AND NOT `+retainedOut+`)`, conv, selfFP); err != nil {
		return err
	}
	// History copies this device forwarded or queued of an erased turn.
	_, err := tx.Exec(`UPDATE outbox SET body = '' WHERE id IN (SELECT o.id FROM outbox o WHERE o.conv = ? AND o.sub = 'history' AND o.body <> '' AND NOT `+retainedOut+`
		AND EXISTS (SELECT 1 FROM conv_erased e WHERE e.conv = o.conv AND e.key = json_extract(o.body, '$.from_key') AND e.lid = json_extract(o.body, '$.lid')))`, conv)
	return err
}

// eraseArrivalIn keeps only the skeleton of inbox row id, just stored,
// when its turn is erased here and no unfinished work needs its text: a
// late copy of an erased turn never shows (views leave erased turns out) and
// keeps no text.
func eraseArrivalIn(tx *sql.Tx, id string) error {
	_, err := tx.Exec(`UPDATE inbox SET body = '' WHERE id IN (SELECT i.id FROM inbox i WHERE i.id = ? AND i.local = 0
		AND coalesce(i.sub, '') IN `+erasableSubs+` AND i.body <> '' AND `+erasedIn+` AND NOT `+retainedIn+`)`, id)
	return err
}

// residualCount counts erased rows of conv whose text no unfinished work
// needs any more but is still here: what eraseCoveredIn would blank now
// (a request whose work has ended, a reply taken, a copy handed over).
func residualCount(q querier, conv, selfFP string) (int, error) {
	var n int
	err := q.QueryRow(`SELECT
		(SELECT count(*) FROM inbox i WHERE coalesce(i.conv, '') = ? AND i.local = 0 AND coalesce(i.sub, '') IN `+erasableSubs+` AND i.body <> '' AND `+erasedIn+` AND NOT `+retainedIn+`)
		+ (SELECT count(*) FROM inbox i JOIN outbox o ON o.id = i.id WHERE i.local = 1 AND coalesce(o.conv, '') = ? AND i.body <> '' AND `+erasedOut+` AND NOT `+retainedIn+`)
		+ (SELECT count(*) FROM outbox o WHERE coalesce(o.conv, '') = ? AND coalesce(o.sub, '') IN `+erasableSubs+` AND o.body <> '' AND `+erasedOut+` AND NOT `+retainedOut+`)
		+ (SELECT count(*) FROM outbox o WHERE o.conv = ? AND o.sub = 'history' AND o.body <> '' AND NOT `+retainedOut+`
		   AND EXISTS (SELECT 1 FROM conv_erased e WHERE e.conv = o.conv AND e.key = json_extract(o.body, '$.from_key') AND e.lid = json_extract(o.body, '$.lid')))`,
		conv, conv, selfFP, conv, selfFP, conv).Scan(&n)
	return n, err
}

// retainedCount counts erased turns of conv kept for unfinished work.
func retainedCount(q querier, conv, selfFP string) (int, error) {
	var n, m int
	if err := q.QueryRow(`SELECT count(*) FROM inbox i WHERE coalesce(i.conv, '') = ? AND i.local = 0 AND i.body <> '' AND `+erasedIn+` AND `+retainedIn, conv).Scan(&n); err != nil {
		return 0, err
	}
	if err := q.QueryRow(`SELECT count(DISTINCT CASE WHEN o.conv IS NULL THEN o.id ELSE o.lid END) FROM outbox o WHERE coalesce(o.conv, '') = ? AND coalesce(o.sub, '') IN `+erasableSubs+`
		AND o.body <> '' AND `+erasedOut+` AND `+retainedOut, conv, selfFP).Scan(&m); err != nil {
		return 0, err
	}
	return n + m, nil
}

// dropErasedFiles removes this device's cached and kept file copies of the
// erased turns of conv whose text is gone: a cached download (ciphertext for
// this device), and a kept copy of a sent file unless a sent turn not erased
// still has it. Saved files are the person's and stay.
func (a *Agent) dropErasedFiles(conv string) {
	selfFP := a.Self().Fingerprint()
	if rows, err := a.store.db.Query(`SELECT a.blob_id FROM attachments a JOIN inbox i ON i.id = a.message_id
		WHERE coalesce(i.conv, '') = ? AND i.local = 0 AND i.body = '' AND `+erasedIn+` AND NOT `+retainedIn, conv); err == nil {
		var blobs []string
		for rows.Next() {
			var blob string
			if rows.Scan(&blob) == nil && !strings.HasPrefix(blob, historyBlob) {
				blobs = append(blobs, blob)
			}
		}
		rows.Close()
		for _, blob := range blobs {
			os.Remove(a.downloadPath(blob))
			os.Remove(a.downloadPath(blob) + ".part")
		}
	}
	rows, err := a.store.db.Query(`SELECT DISTINCT s.sha256 FROM sent_attachments s JOIN outbox o ON o.id = s.message_id
		WHERE coalesce(o.conv, '') = ? AND o.body = '' AND `+erasedOut+` AND NOT `+retainedOut+`
		AND NOT EXISTS (SELECT 1 FROM sent_attachments s2 JOIN outbox o2 ON o2.id = s2.message_id WHERE s2.sha256 = s.sha256
		  AND NOT EXISTS (SELECT 1 FROM conv_erased e WHERE e.conv = coalesce(o2.conv, '') AND e.key = ? AND e.lid = CASE WHEN o2.conv IS NULL THEN o2.id ELSE o2.lid END))`,
		conv, selfFP, selfFP)
	if err != nil {
		return
	}
	var shas []string
	for rows.Next() {
		var sha string
		if rows.Scan(&sha) == nil {
			shas = append(shas, sha)
		}
	}
	rows.Close()
	for _, sha := range shas {
		os.Remove(a.keptPath(sha))
	}
}

// admitClear admits a deletion from another device of this installation's
// own person; from anyone else it is refused (held invalid), never applied.
// It is no conversation act: membership is not asked, nothing is decided.
// It erases exactly the turns it names, nothing else.
func (a *Agent) admitClear(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, fromQuarantine bool, hold func(string, string) error) error {
	var part envelope.Clear
	if err := json.Unmarshal([]byte(in.Body), &part); err != nil || in.Ref == nil {
		return hold(reasonInvalid, "a malformed conversation deletion")
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || !me.has(env.From, sender.Fingerprint()) || env.From == a.Address {
		return hold(reasonInvalid, "a conversation deletion comes only from another current device of this person")
	}
	if _, _, found, err := a.store.conversation(in.Conv); err != nil {
		return err
	} else if !found {
		return hold(reasonProof, "the conversation is not here (yet)")
	}
	selfFP := a.Self().Fingerprint()
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	res, err := tx.Exec(`INSERT OR IGNORE INTO inbox(id, sender, ts, kind, body, received_at, state, verified_by, conv, lid, sub, replica, received_ms, ref_id, ref_fp, read_at, acked)
		VALUES(?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, 1, ?, ?, ?, ?, 1)`,
		in.ID, in.From, in.TS, in.Kind, in.Body, now.Unix(), sender.Fingerprint(), in.Conv, in.LID, in.Sub, now.UnixMilli(), in.Ref.ID, in.Ref.Fingerprint, now.Unix())
	if err != nil {
		return err
	}
	if fromQuarantine {
		if _, err := tx.Exec(`DELETE FROM quarantine WHERE id = ?`, in.ID); err != nil {
			return err
		}
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return tx.Commit() // this copy is applied already
	}
	turns := append([]envelope.Ref{*in.Ref}, part.Turns...)
	for _, t := range turns {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO conv_erased(conv, key, lid, deletion) VALUES(?, ?, ?, ?)`, in.Conv, t.Fingerprint, t.ID, part.Deletion); err != nil {
			return err
		}
	}
	if err := eraseCoveredIn(tx, in.Conv, selfFP); err != nil {
		return err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.dropErasedFiles(in.Conv)
	a.forwardClear(me, in, turns)
	return nil
}

// clearPart is one part of a deletion to send.
type clearPart struct {
	conv, lid string
	ref       envelope.Ref
	body      envelope.Clear
}

// clearParts splits the turns of one deletion into parts: each names its
// first turn as Ref and the rest as Turns, and stands alone.
func clearParts(conv, deletion string, turns []envelope.Ref) []clearPart {
	var parts []clearPart
	for start := 0; start < len(turns); start += envelope.MaxClearTurns + 1 {
		chunk := turns[start:min(start+envelope.MaxClearTurns+1, len(turns))]
		parts = append(parts, clearPart{conv: conv, lid: protocol.NewID(), ref: chunk[0], body: envelope.Clear{Deletion: deletion, Turns: chunk[1:]}})
	}
	for i := range parts {
		parts[i].body.Part, parts[i].body.Parts = i+1, len(parts)
	}
	return parts
}

// clearCopies seals parts for devs (other devices of this person), each
// held until that device reads clr1. The fan names this person's roster:
// the copies go to its devices only.
func (a *Agent) clearCopies(me personRow, devs []identity.Public, parts []clearPart) ([]outCopy, error) {
	var copies []outCopy
	fan := []envelope.Fan{{Person: me.info.Person, Roster: me.info.Roster}}
	for _, p := range parts {
		body, _ := json.Marshal(p.body)
		ref := p.ref
		for _, dev := range devs {
			recipient, err := dev.Recipient()
			if err != nil {
				return nil, err
			}
			in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(),
				Kind: envelope.KindMessage, Sub: envelope.SubClear, Body: string(body), Ref: &ref, Conv: p.conv, LID: p.lid, Replica: true, Fan: fan}
			env, err := envelope.Seal(in, a.id.Sign, recipient)
			if err != nil {
				return nil, err
			}
			copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapConvClear, recipientFP: dev.Fingerprint()})
		}
	}
	return copies, nil
}

// otherDevices are this person's current devices besides this one.
func (a *Agent) otherDevices(me personRow) []identity.Public {
	var out []identity.Public
	for _, d := range me.roster.Devices {
		if d.Address != a.Address {
			out = append(out, d)
		}
	}
	return out
}

// shareErased queues every deletion made here whose turns this person's
// other devices have not been told, marking them told in the same
// transaction; after a crash the next wake does it. It reports how many
// devices the last one was queued for.
func (a *Agent) shareErased(ctx context.Context) (int, error) {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return 0, err
	}
	rows, err := a.store.db.Query(`SELECT DISTINCT deletion, conv FROM conv_erased WHERE shared = 0 AND conv <> '' ORDER BY deletion`)
	if err != nil {
		return 0, err
	}
	type pending struct{ deletion, conv string }
	var all []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.deletion, &p.conv); err != nil {
			rows.Close()
			return 0, err
		}
		all = append(all, p)
	}
	rows.Close()
	devs := a.otherDevices(me)
	for _, p := range all {
		var turns []envelope.Ref
		trows, err := a.store.db.Query(`SELECT key, lid FROM conv_erased WHERE deletion = ? AND shared = 0 ORDER BY key, lid`, p.deletion)
		if err != nil {
			return 0, err
		}
		for trows.Next() {
			var r envelope.Ref
			if err := trows.Scan(&r.Fingerprint, &r.ID); err != nil {
				trows.Close()
				return 0, err
			}
			turns = append(turns, r)
		}
		trows.Close()
		copies, err := a.clearCopies(me, devs, clearParts(p.conv, p.deletion, turns))
		if err != nil {
			return 0, err
		}
		mark := func(tx *sql.Tx, _ string) error {
			res, err := tx.Exec(`UPDATE conv_erased SET shared = 1 WHERE deletion = ? AND shared = 0`, p.deletion)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n != int64(len(turns)) {
				return errShared // another sweep queued it meanwhile: these copies are not stored
			}
			return nil
		}
		if len(copies) == 0 { // no other device yet: a device linked later is told by replayErased
			err = inTx(a.store.db, mark)
		} else {
			err = a.store.addConvOutbox(copies, envelope.Inner{}, mark, "")
		}
		if err != nil && !errors.Is(err, errShared) {
			return 0, err
		}
	}
	if len(all) > 0 {
		a.kickNow() // queued copies go out with the outbox, each when its device reads clr1
	}
	return len(devs), nil
}

// errShared stops queuing a deletion another sweep queued meanwhile.
var errShared = errors.New("conversation deletion queued already")

// inTx runs f in a transaction of its own.
func inTx(db *sql.DB, f func(*sql.Tx, string) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := f(tx, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// forwardClear tells this person's devices that the deleting device did
// not know of (its fan named an older roster) the same turns, as this
// device's own deletion.
func (a *Agent) forwardClear(me personRow, in envelope.Inner, turns []envelope.Ref) {
	var sentTo string
	for _, f := range in.Fan {
		if f.Person == me.info.Person {
			sentTo = f.Roster
		}
	}
	if sentTo == "" || sentTo == me.info.Roster {
		return
	}
	old, ok, err := a.store.chainStep(me.info.Person, sentTo)
	if err != nil || !ok {
		return
	}
	var devs []identity.Public
	for _, d := range me.roster.Devices {
		if d.Address != a.Address && d.Address != in.From && !old.Has(d.Address, d.Fingerprint()) {
			devs = append(devs, d)
		}
	}
	if len(devs) > 0 {
		a.sendErased(me, devs, in.Conv, turns)
	}
}

// replayErased tells dev, a device of this person just linked here, every
// turn erased here, conversation by conversation.
func (a *Agent) replayErased(dev identity.Public) {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return
	}
	rows, err := a.store.db.Query(`SELECT conv, key, lid FROM conv_erased WHERE conv <> '' ORDER BY conv, key, lid`)
	if err != nil {
		return
	}
	byConv := map[string][]envelope.Ref{}
	var convs []string
	for rows.Next() {
		var conv string
		var r envelope.Ref
		if rows.Scan(&conv, &r.Fingerprint, &r.ID) != nil {
			continue
		}
		if _, ok := byConv[conv]; !ok {
			convs = append(convs, conv)
		}
		byConv[conv] = append(byConv[conv], r)
	}
	rows.Close()
	for _, conv := range convs {
		a.sendErased(me, []identity.Public{dev}, conv, byConv[conv])
	}
}

// sendErased queues turns of conv to devs as a deletion of this device's
// own.
func (a *Agent) sendErased(me personRow, devs []identity.Public, conv string, turns []envelope.Ref) {
	copies, err := a.clearCopies(me, devs, clearParts(conv, protocol.NewID(), turns))
	if err == nil && len(copies) > 0 {
		err = a.store.addConvOutbox(copies, envelope.Inner{}, nil, "")
	}
	if err != nil {
		a.Logf("conversation deletion of %s: %v", conv, err)
		return
	}
	a.kickNow()
}

// eraseLoop runs sweepErased now (a restart after a crash) and again after
// every local change, as the worker and reminders look again: the change
// that ends a request (answered, declined, resolved, failed), a reply's
// take or a copy's hand-over erases what was kept for it. No timer.
func (a *Agent) eraseLoop(ctx context.Context) {
	for {
		_, changed := a.Changed() // before looking, so no change is missed
		a.sweepErased(ctx)
		select {
		case <-ctx.Done():
			return
		case <-changed:
		}
	}
}

// sweepErased erases what erased turns kept for work that has since ended
// and queues what is still untold (after a crash).
func (a *Agent) sweepErased(ctx context.Context) {
	selfFP := a.Self().Fingerprint()
	rows, err := a.store.db.Query(`SELECT DISTINCT conv FROM conv_erased`)
	if err != nil {
		return
	}
	var convs []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			convs = append(convs, c)
		}
	}
	rows.Close()
	if len(convs) == 0 {
		return
	}
	for _, conv := range convs {
		if left, err := residualCount(a.store.db, conv, selfFP); err == nil && left > 0 {
			tx, err := a.store.db.Begin()
			if err != nil {
				return
			}
			if err := eraseCoveredIn(tx, conv, selfFP); err != nil {
				tx.Rollback()
				continue
			}
			if a.store.done(tx.Commit()) != nil {
				continue
			}
		}
		a.dropErasedFiles(conv) // also what a crash after the erasing commit left
	}
	if _, err := a.shareErased(ctx); err != nil {
		a.Logf("conversation deletions: %v", err)
	}
}

// withoutErased leaves the device-thread messages with peer erased here out
// of links.
func (s *store) withoutErased(peer, selfFP string, links []link) ([]link, error) {
	rows, err := s.db.Query(`SELECT i.id FROM inbox i WHERE i.sender = ? AND i.conv IS NULL AND `+erasedIn+`
		UNION SELECT o.id FROM outbox o WHERE o.recipient = ? AND o.conv IS NULL AND `+erasedOut, peer, peer, selfFP)
	if err != nil {
		return nil, err
	}
	erased := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		erased[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(erased) == 0 {
		return links, err
	}
	kept := links[:0:0]
	for _, l := range links {
		if !erased[l.id] {
			kept = append(kept, l)
		}
	}
	return kept, nil
}

// convDeleted reports whether conv was deleted here and nothing later is
// left to show: it is then left out of conversation lists until a later
// turn arrives.
func (s *store) convDeleted(conv, selfFP string) bool {
	var deleted, visible int
	s.db.QueryRow(`SELECT count(*) FROM conv_erased WHERE conv = ?`, conv).Scan(&deleted)
	if deleted == 0 {
		return false
	}
	s.db.QueryRow(`SELECT (SELECT count(*) FROM inbox i WHERE i.conv = ? AND i.local = 0 AND i.ref_id IS NULL AND coalesce(i.sub, '') IN `+erasableSubs+` AND NOT `+erasedIn+`)
		+ (SELECT count(*) FROM outbox o WHERE o.conv = ? AND o.ref_id IS NULL AND coalesce(o.sub, '') IN `+erasableSubs+` AND NOT `+erasedOut+`)`, conv, conv, selfFP).Scan(&visible)
	return visible == 0
}
