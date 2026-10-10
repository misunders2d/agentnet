package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Private topic marks (Mark done, Reopen, Archive) follow the person's
// current human devices, as names do (topicsync.go). topic_state remains
// what topic states are derived from; topic_marks keeps the person's latest
// mark per topic (the newest signed time wins, protocol.TopicMark.Newer), and
// topic_mark_copies which mark each other device has or was sent (an empty
// carrier when that device sent it here). Marks are display preferences only:
// shared topic events, receipts and permissions never come from them.
const topicMarkSyncSchema = `
CREATE TABLE topic_marks(owner TEXT NOT NULL, scope TEXT NOT NULL, topic TEXT NOT NULL, mark TEXT NOT NULL, count INTEGER NOT NULL, at INTEGER NOT NULL, writer TEXT NOT NULL, PRIMARY KEY(owner,scope,topic));
CREATE TABLE topic_mark_copies(owner TEXT NOT NULL, recipient_fp TEXT NOT NULL, scope TEXT NOT NULL, topic TEXT NOT NULL, mark TEXT NOT NULL, count INTEGER NOT NULL, at INTEGER NOT NULL, writer TEXT NOT NULL, carrier TEXT NOT NULL, PRIMARY KEY(owner,recipient_fp,scope,topic));
`

// topicMarkSeeded is the config key saying owner's marks set here before
// they synced are recorded (once, at their own time).
func topicMarkSeeded(owner string) string { return "topic-marks-seeded/" + owner }

// writeTopicStateMark applies m to the local topic state, keeping its name.
func writeTopicStateMark(tx *sql.Tx, m protocol.TopicMark) error {
	_, err := tx.Exec(`INSERT INTO topic_state(peer,topic,mark,mark_at,mark_count,updated_at) VALUES(?,?,nullif(?,''),CASE WHEN ?='' THEN NULL ELSE ? END,nullif(?,0),?)
		ON CONFLICT(peer,topic) DO UPDATE SET mark=excluded.mark,mark_at=excluded.mark_at,mark_count=excluded.mark_count,updated_at=excluded.updated_at`,
		m.Scope, m.Topic, m.Mark, m.Mark, m.At, m.Count, storeNow().Unix())
	return err
}

// saveTopicMark makes m owner's mark on its topic when it is newer than the
// one recorded, and reports whether it did.
func saveTopicMark(tx *sql.Tx, owner string, m protocol.TopicMark) (bool, error) {
	old, found, err := topicMarkIn(tx, `topic_marks WHERE owner=? AND scope=? AND topic=?`, owner, m.Scope, m.Topic)
	if err != nil || found && !m.Newer(old) {
		return false, err
	}
	if _, err = tx.Exec(`INSERT INTO topic_marks(owner,scope,topic,mark,count,at,writer) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(owner,scope,topic) DO UPDATE SET mark=excluded.mark,count=excluded.count,at=excluded.at,writer=excluded.writer`,
		owner, m.Scope, m.Topic, m.Mark, m.Count, m.At, m.Writer); err != nil {
		return false, err
	}
	return true, writeTopicStateMark(tx, m)
}

func topicMarkIn(q dbq, from string, args ...any) (protocol.TopicMark, bool, error) {
	var m protocol.TopicMark
	err := q.QueryRow(`SELECT scope,topic,mark,count,at,writer FROM `+from, args...).Scan(&m.Scope, &m.Topic, &m.Mark, &m.Count, &m.At, &m.Writer)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

// ownTopicWriter is the person this device records marks and names for: its
// own current human person, or "" (marks then stay on this device).
func (a *Agent) ownTopicWriter(q dbq) (string, error) {
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil || !ok {
		return "", err
	}
	fp := a.Self().Fingerprint()
	if !me.has(a.Address, fp) || !me.roster.Human(fp) {
		return "", nil
	}
	return me.info.Person, nil
}

// seedTopicMarks records, once per person, the marks this device set before
// marks synced, at the time each was set: devices then converge on the
// latest of them rather than on whichever arrived first.
func seedTopicMarks(tx *sql.Tx, owner, fp string) (bool, error) {
	var done string
	err := tx.QueryRow(`SELECT v FROM config WHERE k=?`, topicMarkSeeded(owner)).Scan(&done)
	if err == nil || !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	rows, err := tx.Query(`SELECT peer,topic,coalesce(mark,''),coalesce(mark_count,0),coalesce(mark_at,0) FROM topic_state WHERE coalesce(mark,'')!=''`)
	if err != nil {
		return false, err
	}
	var marks []protocol.TopicMark
	for rows.Next() {
		m := protocol.TopicMark{Writer: fp}
		if err = rows.Scan(&m.Scope, &m.Topic, &m.Mark, &m.Count, &m.At); err != nil {
			rows.Close()
			return false, err
		}
		if m.Valid() { // a malformed old row stays here only
			marks = append(marks, m)
		}
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return false, err
	}
	for _, m := range marks {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO topic_marks(owner,scope,topic,mark,count,at,writer) VALUES(?,?,?,?,?,?,?)`, owner, m.Scope, m.Topic, m.Mark, m.Count, m.At, m.Writer); err != nil {
			return false, err
		}
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO config(k,v) VALUES(?,'1')`, topicMarkSeeded(owner))
	return true, err
}

// setTopicMark sets mark (covering count items; none clears it) on topic in
// scope here, in tx, and returns its time. On a current own human device it
// becomes the person's mark for their other human devices too: its time is
// now, but past every mark known for the topic, so this explicit choice
// replaces the one it was made over.
func (a *Agent) setTopicMark(tx *sql.Tx, scope, topic, mark string, count int) (int64, error) {
	m := protocol.TopicMark{Scope: scope, Topic: topic, Mark: mark, Count: int64(count), At: storeNow().Unix(), Writer: a.Self().Fingerprint()}
	if mark == protocol.TopicMarkNone {
		m.Count = 0
	}
	owner, err := a.ownTopicWriter(tx)
	if err != nil {
		return 0, err
	}
	if owner != "" {
		var prev int64
		if err = tx.QueryRow(`SELECT coalesce(max(at),0) FROM topic_marks WHERE owner=? AND scope=? AND topic=?`, owner, scope, topic).Scan(&prev); err != nil {
			return 0, err
		}
		m.At = max(m.At, prev+1)
	}
	if owner == "" || !m.Valid() {
		return m.At, writeTopicStateMark(tx, m)
	}
	_, err = saveTopicMark(tx, owner, m)
	return m.At, err
}

// changeTopicMark is setTopicMark in its own transaction, followed by sync.
func (a *Agent) changeTopicMark(scope, topic, mark string, count int) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = a.setTopicMark(tx, scope, topic, mark, count); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.topicTitlesChanged()
	}
	return err
}

// syncTopicMarks seals the person's marks that each other current human
// device lacks, one bounded page per sync, and reports whether more follow.
func (a *Agent) syncTopicMarks() (bool, error) {
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return false, err
	}
	defer release()
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	me, ok, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil || !ok {
		return false, err
	}
	fp := a.Self().Fingerprint()
	if !me.has(a.Address, fp) || !me.roster.Human(fp) {
		return false, nil
	}
	seeded, err := seedTopicMarks(tx, me.info.Person, fp)
	if err != nil {
		return false, err
	}
	more := false
	var copies []outCopy
	var sent [][]protocol.TopicMark
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		if full, e := syncWindowFull(tx, dev); e != nil {
			return false, e
		} else if full {
			continue
		}
		r := protocol.TopicStateSync{V: 1, Person: me.info.Person, Roster: me.info.Roster}
		if topicSyncAuthority(tx, protocol.TopicSync{Person: r.Person, Roster: r.Roster}, a.Address, fp, dev.Address, dev.Fingerprint()) != nil {
			continue
		}
		// A device that cannot read marks yet keeps one waiting carrier;
		// later marks stay unsealed here until it reads them, so nothing
		// piles up for an older program.
		var waiting int
		if err = tx.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND recipient_fp=? AND sub=? AND state=?`, dev.Address, dev.Fingerprint(), envelope.SubTopicStateSync, stateConvWaiting).Scan(&waiting); err != nil {
			return false, err
		}
		if waiting > 0 {
			continue
		}
		// A held carrier retains its exact facts; only a different mark
		// needs another copy.
		rows, e := tx.Query(`SELECT m.scope,m.topic,m.mark,m.count,m.at,m.writer FROM topic_marks m WHERE m.owner=? AND NOT EXISTS(SELECT 1 FROM topic_mark_copies c LEFT JOIN outbox o ON o.id=c.carrier WHERE c.owner=m.owner AND c.recipient_fp=? AND c.scope=m.scope AND c.topic=m.topic AND c.mark=m.mark AND c.count=m.count AND c.at=m.at AND c.writer=m.writer AND (c.carrier='' OR o.state IN ('queued','waiting','custody','delivered','quarantined'))) ORDER BY m.scope,m.topic LIMIT ?`, me.info.Person, dev.Fingerprint(), protocol.MaxTopicMarks)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var m protocol.TopicMark
			if e = rows.Scan(&m.Scope, &m.Topic, &m.Mark, &m.Count, &m.At, &m.Writer); e != nil {
				break
			}
			r.Marks = append(r.Marks, m)
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if rowErr != nil {
			return false, rowErr
		}
		if len(r.Marks) == 0 {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, e := dev.Recipient()
		if e != nil {
			return false, e
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubTopicStateSync, Replica: true, Body: string(raw)}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return false, e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapTopicStateSync, recipientFP: dev.Fingerprint()})
		sent = append(sent, r.Marks)
		more = more || len(r.Marks) == protocol.MaxTopicMarks
		if len(copies) == historyPage {
			more = true
			break
		}
	}
	if len(copies) == 0 && !seeded {
		return false, nil
	}
	if err = insertCopies(tx, copies); err != nil {
		return false, err
	}
	for i, c := range copies {
		for _, m := range sent[i] {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO topic_mark_copies(owner,recipient_fp,scope,topic,mark,count,at,writer,carrier) VALUES(?,?,?,?,?,?,?,?,?)`, me.info.Person, c.recipientFP, m.Scope, m.Topic, m.Mark, m.Count, m.At, m.Writer, c.env.ID); err != nil {
				return false, err
			}
		}
	}
	return more, a.store.done(tx.Commit())
}

func (a *Agent) admitTopicStateSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, err := protocol.ParseTopicStateSync([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person {
		return hold(reasonInvalid, "topic marks belong to another person")
	}
	if err = a.refreshRecipientPerson(ctx, env.From, map[string]error{}); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	auth := protocol.TopicSync{Person: r.Person, Roster: r.Roster}
	if err = topicSyncAuthority(tx, auth, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	// This device's own earlier marks compete at their own time.
	if _, err = seedTopicMarks(tx, r.Person, a.Self().Fingerprint()); err != nil {
		return err
	}
	for _, m := range r.Marks {
		if _, err = saveTopicMark(tx, r.Person, m); err != nil {
			return err
		}
		// The sender has this mark: it needs no copy of it back.
		had, found, e := topicMarkIn(tx, `topic_mark_copies WHERE owner=? AND recipient_fp=? AND scope=? AND topic=?`, r.Person, sender.Fingerprint(), m.Scope, m.Topic)
		if e != nil {
			return e
		}
		if !found || m.Newer(had) {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO topic_mark_copies(owner,recipient_fp,scope,topic,mark,count,at,writer,carrier) VALUES(?,?,?,?,?,?,?,?,'')`, r.Person, sender.Fingerprint(), m.Scope, m.Topic, m.Mark, m.Count, m.At, m.Writer); err != nil {
				return err
			}
		}
	}
	if held {
		if _, err = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
			return err
		}
	}
	if err = receiptCarrier(tx, env.ID); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.convWork.due(convHistory)
		a.kickNow()
	}
	return err
}

func (a *Agent) mayDeliverTopicStateSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubTopicStateSync {
		return false, false, nil
	}
	if err != nil {
		return true, false, err
	}
	if state != stateQueued {
		return true, false, nil
	}
	if err = a.refreshRecipientPerson(context.Background(), env.To, map[string]error{}); err != nil {
		return true, false, err
	}
	r, parseErr := protocol.ParseTopicStateSync([]byte(body))
	auth := protocol.TopicSync{Person: r.Person, Roster: r.Roster}
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parseErr == nil && found && pending == nil && key.Fingerprint() == fp && topicSyncAuthority(a.store.db, auth, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "topic mark owner or device authority changed", "")
		}
		return key, ok, e
	}
	key, ok, err := check()
	if err != nil || !ok {
		return true, false, err
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapTopicStateSync); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	// Capability lookup performs I/O; recheck exact current authority afterward.
	_, ok, err = check()
	return true, ok, err
}
