package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Conversation history across one person's devices (owner decision
// 2026-09-29: the same chats, with their history, on every device).
//
// A device of the person forwards what it holds to another device of the
// same person as history: an envelope (sub "history", a replica) from that
// device, carrying one HistoryItem: a message as it was sent or received,
// with the key it was sent under (claimed: the forwarding device vouches for
// it; the original signature covered ciphertext for another device, so it
// cannot travel). Roots travel with every envelope and keep their creator's
// signature; participation events keep their author's.
//
// Two paths, both through the outbox (durable, receipted, resumed after a
// restart):
//
//   - the snapshot: after this device links a new device of its person,
//     it walks its conversation rows in order, a page per sync (saved
//     position, same transaction as the page queued), until it reaches the
//     end;
//   - forwarding: a device that admits a message sent to an older roster of
//     its person (fan) forwards it to the devices that roster lacks, in the
//     same transaction as it admits it. Every such device forwards; copies
//     beyond the first are duplicates, stored once.
//
// A history copy never runs, answers, alerts or reminds; it is shown as
// synced from the device that forwarded it, and gives way to a copy of the
// same message received directly (convstore.go: addConvInbox).

// HistoryItem is one message (or participation event, as the message that
// carried it) forwarded as history.
type HistoryItem struct {
	V           int                   `json:"v"`
	From        string                `json:"from"`
	FromKey     string                `json:"from_key"` // the key it was sent under (claimed)
	ID          string                `json:"id"`
	LID         string                `json:"lid"`
	TS          int64                 `json:"ts"`
	Kind        string                `json:"kind"`
	Body        string                `json:"body"`
	ReplyTo     string                `json:"reply_to,omitempty"`
	Status      string                `json:"status,omitempty"`
	Sub         string                `json:"sub,omitempty"`
	Origin      string                `json:"origin,omitempty"`
	Emotion     string                `json:"emotion,omitempty"`
	Target      *envelope.Target      `json:"target,omitempty"`
	PID         string                `json:"pid,omitempty"`
	Attachments []envelope.Attachment `json:"attachments,omitempty"` // manifests (name, size, sha256); no blob for the new device
}

// inner is the item as the message it records.
func (h HistoryItem) inner(conv string) envelope.Inner {
	in := envelope.Inner{V: envelope.Version2, ID: h.ID, From: h.From, TS: h.TS, Kind: h.Kind, Body: h.Body, ReplyTo: h.ReplyTo,
		Status: h.Status, Sub: h.Sub, Origin: h.Origin, Emotion: h.Emotion, Target: h.Target, PID: h.PID, Conv: conv, LID: h.LID, Replica: true}
	for _, a := range h.Attachments {
		in.Attachments = append(in.Attachments, envelope.Attachment{Name: a.Name, Size: a.Size, SHA256: a.SHA256})
	}
	return in
}

func itemOf(in envelope.Inner, key string) HistoryItem {
	h := HistoryItem{V: 1, From: in.From, FromKey: key, ID: in.ID, LID: in.LID, TS: in.TS, Kind: in.Kind, Body: in.Body, ReplyTo: in.ReplyTo,
		Status: in.Status, Sub: in.Sub, Origin: in.Origin, Emotion: in.Emotion, Target: in.Target, PID: in.PID}
	for _, a := range in.Attachments {
		h.Attachments = append(h.Attachments, envelope.Attachment{Name: a.Name, Size: a.Size, SHA256: a.SHA256})
	}
	return h
}

// historyCopy seals item as history for the device to, in conversation
// conv (root raw).
func (a *Agent) historyCopy(to identity.Public, conv string, raw []byte, item HistoryItem) (outCopy, error) {
	recipient, err := to.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	body, _ := json.Marshal(item)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: to.Address, TS: time.Now().Unix(),
		Kind: envelope.KindMessage, Body: string(body), Conv: conv, LID: protocol.NewID(), Root: raw, Replica: true, Sub: envelope.SubHistory}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	if err != nil {
		return outCopy{}, err
	}
	return outCopy{env: env, in: in, state: stateQueued}, nil
}

// insertCopies stores history copies in the outbox, within tx.
func insertCopies(tx *sql.Tx, copies []outCopy) error {
	now := time.Now()
	for _, c := range copies {
		data, _ := json.Marshal(c.env)
		if _, err := tx.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at, conv, lid, kind, created_ms, sub)
			VALUES(?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.env.ID, c.env.To, string(data), c.state, now.Unix(), c.in.Conv, c.in.LID, c.in.Kind, now.UnixMilli(), c.in.Sub); err != nil {
			return err
		}
	}
	return nil
}

// forwardStale returns history copies of in (admitted from a device of
// person p, verified under key) for the current devices of this
// installation's person that the roster the sender sent to (fan) lacks:
// copies the sender could not know to send.
func (a *Agent) forwardStale(me personRow, in envelope.Inner, key string, raw []byte) []outCopy {
	var sentTo string
	for _, f := range in.Fan {
		if f.Person == me.info.Person {
			sentTo = f.Roster
		}
	}
	if sentTo == "" || sentTo == me.info.Roster {
		return nil
	}
	old, ok, err := a.store.chainStep(me.info.Person, sentTo)
	if err != nil || !ok {
		return nil // a roster not in this person's chain: nothing to go by
	}
	item := itemOf(in, key)
	var copies []outCopy
	for _, d := range me.roster.Devices {
		if d.Address == a.Address || old.Has(d.Address, d.Fingerprint()) || d.Address == in.From {
			continue
		}
		c, err := a.historyCopy(d, in.Conv, raw, item)
		if err != nil {
			a.Logf("forwarding %s to %s: %v", in.ID, d.Address, err)
			continue
		}
		copies = append(copies, c)
	}
	return copies
}

// admitHistory admits a history envelope from sender, a current device of
// this installation's own person (checked by the caller with the root):
// its item is stored as history if its claimed sender device is (or was)
// a device of a member of the conversation, as that member's pinned chain
// lists it.
func (a *Agent) admitHistory(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, hold func(string, string) error, fromQuarantine bool) error {
	var item HistoryItem
	if err := decodeStrict([]byte(in.Body), &item); err != nil || item.V != 1 || !protocol.ValidID(item.ID) || !protocol.ValidID(item.LID) {
		return hold(reasonInvalid, "a malformed history item")
	}
	owner := ""
	var key identity.Public
	for pass := 0; pass < 2 && owner == ""; pass++ {
		for _, m := range root.Members {
			if pass == 1 {
				if _, err := a.refreshPerson(ctx, m.Person, false); err != nil && !errors.Is(err, errPersonConflict) {
					return err
				}
			}
			if k, ok := a.store.deviceKey(m.Person, item.From, item.FromKey); ok {
				owner, key = m.Person, k
				break
			}
		}
	}
	if owner == "" {
		return hold(reasonInvalid, "the history item's sender is no device of a member of this conversation")
	}
	if p, ok, err := a.store.personByID(owner); err != nil {
		return err
	} else if ok && p.info.State == personConflict {
		return hold(reasonConflict, errPersonConflict.Error())
	}
	orig := item.inner(in.Conv)
	var also func(*sql.Tx) error
	if orig.Sub == envelope.SubEvent { // the signed event itself, verified under its author's key
		ev, err := checkParticipationEvent(orig, key.Fingerprint(), key.SignKey)
		if err != nil {
			return hold(reasonInvalid, err.Error())
		}
		raw := []byte(orig.Body)
		also = func(tx *sql.Tx) error { return insertParticipationEvent(tx, ev, raw) }
	}
	res, err := a.store.addHistoryInbox(orig, item.FromKey, env.From, env.ID, fromQuarantine, also)
	if errors.Is(err, errTooManyEvents) {
		return hold(reasonInvalid, err.Error())
	}
	if err == nil && res == admitted && orig.Sub == envelope.SubEvent {
		a.wakeWorker() // a participation may resolve differently; nothing here runs from history
	}
	return err
}

// History snapshot jobs (history_jobs): one per new device of this person
// that this device linked.

// historyPos is where a snapshot continues: after the row (conv, ms, id).
type historyPos struct {
	Conv string `json:"conv"`
	Ms   int64  `json:"ms"`
	ID   string `json:"id"`
}

const historyPage = 50

// startHistory queues a snapshot of this device's conversations for dev.
func (a *Agent) startHistory(dev identity.Public) error {
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM conversations`).Scan(&n); err != nil {
		return err
	}
	now := time.Now().Unix()
	pos, _ := json.Marshal(historyPos{})
	if _, err := a.store.db.Exec(`INSERT OR IGNORE INTO history_jobs(device, fingerprint, pos, convs_total, state, created_at, updated_at) VALUES(?, ?, ?, ?, 'running', ?, ?)`,
		dev.Address, dev.Fingerprint(), string(pos), n, now, now); err != nil {
		return err
	}
	a.convWork.due(convHistory)
	a.kickNow()
	return nil
}

// kickNow asks the current stream's worker to sync now; safe from any
// goroutine (a page's request, the stream reader).
func (a *Agent) kickNow() {
	a.kickMu.Lock()
	k := a.kick
	a.kickMu.Unlock()
	if k != nil {
		k()
	}
}

// historyStep queues one page of every running snapshot and reports
// whether any has more.
func (a *Agent) historyStep(ctx context.Context) (more bool) {
	rows, err := a.store.db.Query(`SELECT device, fingerprint, pos FROM history_jobs WHERE state = 'running'`)
	if err != nil {
		return false
	}
	type job struct {
		device, fp string
		pos        historyPos
	}
	var jobs []job
	for rows.Next() {
		var j job
		var pos string
		if rows.Scan(&j.device, &j.fp, &pos) == nil && json.Unmarshal([]byte(pos), &j.pos) == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return false
	}
	for _, j := range jobs {
		dev, ok := me.device(j.device)
		if !ok || dev.Fingerprint() != j.fp {
			a.store.db.Exec(`UPDATE history_jobs SET state = 'ended', updated_at = ? WHERE device = ?`, time.Now().Unix(), j.device)
			continue // no longer a device of this person
		}
		m, err := a.historyPageFor(dev, j.pos)
		if err != nil {
			a.Logf("history for %s: %v", j.device, err)
			continue
		}
		more = more || m
	}
	return more
}

// historyPageFor queues the next page of history for dev after pos, and
// saves where it ended in the same transaction.
func (a *Agent) historyPageFor(dev identity.Public, pos historyPos) (more bool, err error) {
	self := a.Self().Fingerprint()
	rows, err := a.store.db.Query(`
		SELECT conv, ms, id, 'in', sender, coalesce(verified_by, claimed_fp, ''), ts, kind, body, coalesce(reply_to, ''), coalesce(status, ''), coalesce(sub, ''),
		       coalesce(origin, ''), coalesce(emotion, ''), coalesce(target, ''), coalesce(pid, ''), lid FROM (
		  SELECT conv, received_ms AS ms, id, sender, verified_by, claimed_fp, ts, kind, body, reply_to, status, sub, origin, emotion, target, pid, lid
		    FROM inbox WHERE conv IS NOT NULL AND local = 0 AND coalesce(sub, '') != 'history'
		  UNION ALL
		  SELECT o.conv, o.created_ms, o.id, ?, ?, NULL, o.created_at, o.kind, o.body, o.reply_to, o.status, o.sub, o.origin, o.emotion, o.target, o.pid, o.lid
		    FROM outbox o WHERE o.conv IS NOT NULL AND coalesce(o.sub, '') != 'history'
		     AND o.rowid = (SELECT min(rowid) FROM outbox f WHERE f.conv = o.conv AND f.lid = o.lid))
		WHERE (conv, ms, id) > (?, ?, ?) AND conv IN (SELECT id FROM conversations)
		ORDER BY conv, ms, id LIMIT ?`, a.Address, self, pos.Conv, pos.Ms, pos.ID, historyPage)
	if err != nil {
		return false, err
	}
	var items []struct {
		conv string
		in   envelope.Inner
		key  string
		pos  historyPos
	}
	for rows.Next() {
		var conv, dir, key, target string
		var ms int64
		var in envelope.Inner
		if err := rows.Scan(&conv, &ms, &in.ID, &dir, &in.From, &key, &in.TS, &in.Kind, &in.Body, &in.ReplyTo, &in.Status, &in.Sub,
			&in.Origin, &in.Emotion, &target, &in.PID, &in.LID); err != nil {
			rows.Close()
			return false, err
		}
		if target != "" {
			in.Target = &envelope.Target{}
			json.Unmarshal([]byte(target), in.Target)
		}
		in.Conv = conv
		items = append(items, struct {
			conv string
			in   envelope.Inner
			key  string
			pos  historyPos
		}{conv, in, key, historyPos{conv, ms, in.ID}})
	}
	rows.Close()
	var copies []outCopy
	roots := map[string][]byte{}
	for _, it := range items {
		files, err := a.store.attachmentManifest(it.in.ID)
		if err != nil {
			return false, err
		}
		it.in.Attachments = files
		raw, ok := roots[it.conv]
		if !ok {
			_, raw, _, err = a.store.conversation(it.conv)
			if err != nil {
				return false, err
			}
			roots[it.conv] = raw
		}
		c, err := a.historyCopy(dev, it.conv, raw, itemOf(it.in, it.key))
		if err != nil {
			return false, err
		}
		copies = append(copies, c)
	}
	next, state := pos, "running"
	if len(items) > 0 {
		next = items[len(items)-1].pos
	}
	if len(items) < historyPage {
		state = "done"
	}
	data, _ := json.Marshal(next)
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := insertCopies(tx, copies); err != nil {
		return false, err
	}
	if _, err := tx.Exec(`UPDATE history_jobs SET pos = ?, state = ?, updated_at = ? WHERE device = ?`, string(data), state, time.Now().Unix(), dev.Address); err != nil {
		return false, err
	}
	if err := a.store.done(tx.Commit()); err != nil {
		return false, err
	}
	if len(copies) > 0 {
		notifyDaemon(a.home)
	}
	return state == "running", nil
}

// attachmentManifest returns the files of a stored message (received or
// sent), as manifests.
func (s *store) attachmentManifest(id string) ([]envelope.Attachment, error) {
	rows, err := s.db.Query(`SELECT name, size, sha256 FROM attachments WHERE message_id = ?
		UNION ALL SELECT name, size, sha256 FROM sent_attachments WHERE message_id = ? ORDER BY 1`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []envelope.Attachment
	for rows.Next() {
		var a envelope.Attachment
		if err := rows.Scan(&a.Name, &a.Size, &a.SHA256); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// HistoryJob is the progress of sending this device's conversations to a
// new device of its person.
type HistoryJob struct {
	Device     string `json:"device"`
	Name       string `json:"name"`
	ConvsDone  int    `json:"convs_done"`
	ConvsTotal int    `json:"convs_total"`
	State      string `json:"state"` // running (queued as this device's connection allows), done, ended (the device left)
}

// HistoryProgress lists the history snapshots this device sends or sent.
// Queued pages still go out through the outbox: "done" means all is
// queued; delivery follows as the Hub takes it (this device must stay
// connected until then).
func (a *Agent) HistoryProgress() ([]HistoryJob, error) {
	rows, err := a.store.db.Query(`SELECT device, pos, convs_total, state FROM history_jobs ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryJob
	for rows.Next() {
		var j HistoryJob
		var pos string
		if err := rows.Scan(&j.Device, &pos, &j.ConvsTotal, &j.State); err != nil {
			return nil, err
		}
		_, j.Name, _ = protocol.SplitAddress(j.Device)
		var p historyPos
		json.Unmarshal([]byte(pos), &p)
		if j.State == "done" {
			j.ConvsDone = j.ConvsTotal
		} else if p.Conv != "" {
			a.store.db.QueryRow(`SELECT count(*) FROM conversations WHERE id < ?`, p.Conv).Scan(&j.ConvsDone)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
