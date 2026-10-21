package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
	"time"
)

// Reminder intent is shared; local rev/notified_rev remain the once-per-winning-
// generation delivery ledger. Terminal facts are retained to defeat stale replay.
const reminderSyncSchema = `
ALTER TABLE reminders ADD COLUMN fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE reminders ADD COLUMN lid TEXT NOT NULL DEFAULT '';
ALTER TABLE reminders ADD COLUMN sync_revision INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reminders ADD COLUMN sync_writer TEXT NOT NULL DEFAULT '';
ALTER TABLE reminders ADD COLUMN sync_dirty INTEGER NOT NULL DEFAULT 0;
CREATE INDEX reminder_ref ON reminders(conv,fingerprint,lid);
CREATE TABLE reminder_marks(owner TEXT NOT NULL,conv TEXT NOT NULL,fingerprint TEXT NOT NULL,lid TEXT NOT NULL,due INTEGER NOT NULL,state TEXT NOT NULL,revision INTEGER NOT NULL,writer TEXT NOT NULL,PRIMARY KEY(owner,conv,fingerprint,lid));
CREATE TABLE reminder_mark_copies(owner TEXT NOT NULL,recipient_fp TEXT NOT NULL,conv TEXT NOT NULL,fingerprint TEXT NOT NULL,lid TEXT NOT NULL,due INTEGER NOT NULL,state TEXT NOT NULL,revision INTEGER NOT NULL,writer TEXT NOT NULL,carrier TEXT NOT NULL,PRIMARY KEY(owner,recipient_fp,conv,fingerprint,lid));
`

func reminderMarkIn(q dbq, from string, args ...any) (protocol.ReminderMark, bool, error) {
	var m protocol.ReminderMark
	err := q.QueryRow(`SELECT conv,fingerprint,lid,due,state,revision,writer FROM `+from, args...).Scan(&m.Conv, &m.Fingerprint, &m.LID, &m.Due, &m.State, &m.Revision, &m.Writer)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}
func reminderRef(q dbq, id, fp string) (protocol.ReminderMark, error) {
	var m protocol.ReminderMark
	err := q.QueryRow(`SELECT coalesce(conv,''),coalesce(verified_by,claimed_fp,''),coalesce(nullif(lid,''),id) FROM inbox WHERE id=? UNION ALL SELECT coalesce(conv,''),?,coalesce(nullif(lid,''),id) FROM outbox WHERE id=? LIMIT 1`, id, fp, id).Scan(&m.Conv, &m.Fingerprint, &m.LID)
	return m, err
}

// A local view may address another physical copy of the same original.
func (a *Agent) reminderLocalID(id string) string {
	m, err := reminderRef(a.store.db, id, a.Self().Fingerprint())
	if err != nil {
		return id
	}
	var canonical string
	if a.store.db.QueryRow(`SELECT message FROM reminders WHERE coalesce(conv,'')=? AND fingerprint=? AND lid=? ORDER BY message LIMIT 1`, m.Conv, m.Fingerprint, m.LID).Scan(&canonical) == nil {
		return canonical
	}
	return id
}

var errReminderGenerationConflict = errors.New("reminder sync: conflicting generation")

func saveReminderMark(tx *sql.Tx, owner string, m protocol.ReminderMark) (bool, error) {
	old, found, err := reminderMarkIn(tx, `reminder_marks WHERE owner=? AND conv=? AND fingerprint=? AND lid=?`, owner, m.Conv, m.Fingerprint, m.LID)
	if err != nil {
		return false, err
	}
	if found && !m.Newer(old) {
		if m.Revision == old.Revision && m.Writer == old.Writer && (m.Due != old.Due || m.State != old.State) {
			return false, errReminderGenerationConflict
		}
		return false, nil
	}
	_, err = tx.Exec(`INSERT INTO reminder_marks(owner,conv,fingerprint,lid,due,state,revision,writer) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(owner,conv,fingerprint,lid) DO UPDATE SET due=excluded.due,state=excluded.state,revision=excluded.revision,writer=excluded.writer`, owner, m.Conv, m.Fingerprint, m.LID, m.Due, m.State, m.Revision, m.Writer)
	return err == nil, err
}
func putReminder(tx *sql.Tx, id string, m protocol.ReminderMark) error {
	_, err := tx.Exec(`INSERT INTO reminders(message,conv,due_at,state,rev,notified_rev,created_at,updated_at,fingerprint,lid,sync_revision,sync_writer) VALUES(?,nullif(?,''),?,?,1,0,?,?,?,?,?,?) ON CONFLICT(message) DO UPDATE SET due_at=excluded.due_at,state=excluded.state,rev=reminders.rev+CASE WHEN reminders.sync_revision!=excluded.sync_revision OR reminders.sync_writer!=excluded.sync_writer THEN 1 ELSE 0 END,updated_at=excluded.updated_at,fingerprint=excluded.fingerprint,lid=excluded.lid,sync_revision=excluded.sync_revision,sync_writer=excluded.sync_writer,sync_dirty=0`, id, m.Conv, m.Due, m.State, time.Now().Unix(), time.Now().Unix(), m.Fingerprint, m.LID, m.Revision, m.Writer)
	return err
}

// Capture old local rows once, at their recorded time. The durable dirty bit
// also covers automatic replies in the same transaction that stores the reply.
func captureReminders(tx *sql.Tx, owner, fp string) (bool, error) {
	rows, err := tx.Query(`SELECT message,due_at,state,updated_at,sync_dirty FROM reminders WHERE sync_revision=0 OR sync_dirty=1`)
	if err != nil {
		return false, err
	}
	type row struct {
		id, state string
		due, at   int64
		dirty     bool
	}
	var local []row
	for rows.Next() {
		var r row
		if err = rows.Scan(&r.id, &r.due, &r.state, &r.at, &r.dirty); err != nil {
			break
		}
		local = append(local, r)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if rowErr != nil {
		return false, rowErr
	}
	changed := false
	for _, r := range local {
		m, e := reminderRef(tx, r.id, fp)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return false, e
		}
		m.Due = r.due
		m.State = r.state
		m.Revision = max(1, r.at*1000)
		m.Writer = fp
		old, _, e := reminderMarkIn(tx, `reminder_marks WHERE owner=? AND conv=? AND fingerprint=? AND lid=?`, owner, m.Conv, m.Fingerprint, m.LID)
		if e != nil {
			return false, e
		}
		if r.dirty {
			m.Revision = max(m.Revision, old.Revision+1)
		}
		if !m.Valid() {
			continue
		}
		if _, e = saveReminderMark(tx, owner, m); e != nil {
			return false, e
		}
		if _, e = tx.Exec(`UPDATE reminders SET fingerprint=?,lid=?,sync_revision=?,sync_writer=?,sync_dirty=0 WHERE message=?`, m.Fingerprint, m.LID, m.Revision, m.Writer, r.id); e != nil {
			return false, e
		}
		changed = true
	}
	projected, err := projectReminders(tx, owner, fp)
	return changed || projected, err
}
func projectReminders(tx *sql.Tx, owner, fp string) (bool, error) {
	rows, err := tx.Query(`SELECT conv,fingerprint,lid,due,state,revision,writer FROM reminder_marks WHERE owner=?`, owner)
	if err != nil {
		return false, err
	}
	var marks []protocol.ReminderMark
	for rows.Next() {
		var m protocol.ReminderMark
		if err = rows.Scan(&m.Conv, &m.Fingerprint, &m.LID, &m.Due, &m.State, &m.Revision, &m.Writer); err != nil {
			break
		}
		marks = append(marks, m)
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if rowErr != nil {
		return false, rowErr
	}
	changed := false
	for _, m := range marks {
		var id string
		// Prefer an existing local reminder so a second physical copy cannot alert twice.
		err = tx.QueryRow(`SELECT message FROM reminders WHERE coalesce(conv,'')=? AND fingerprint=? AND lid=? ORDER BY message LIMIT 1`, m.Conv, m.Fingerprint, m.LID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRow(`SELECT id FROM inbox WHERE coalesce(conv,'')=? AND coalesce(verified_by,claimed_fp,'')=? AND coalesce(nullif(lid,''),id)=? AND local=0 UNION ALL SELECT id FROM outbox WHERE coalesce(conv,'')=? AND ?=? AND coalesce(nullif(lid,''),id)=? AND coalesce(sub,'')='' LIMIT 1`, m.Conv, m.Fingerprint, m.LID, m.Conv, m.Fingerprint, fp, m.LID).Scan(&id)
		}
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		var revision int64
		var writer string
		err = tx.QueryRow(`SELECT sync_revision,sync_writer FROM reminders WHERE message=?`, id).Scan(&revision, &writer)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if revision != m.Revision || writer != m.Writer {
			if err = putReminder(tx, id, m); err != nil {
				return false, err
			}
			changed = true
		}
		// An upgrade may find more than one legacy physical alias of this original.
		if _, err = tx.Exec(`DELETE FROM reminders WHERE coalesce(conv,'')=? AND fingerprint=? AND lid=? AND message!=?`, m.Conv, m.Fingerprint, m.LID, id); err != nil {
			return false, err
		}
	}
	return changed, nil
}
func (a *Agent) reconcileReminders() error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	owner, err := a.ownTopicWriter(tx)
	if err != nil || owner == "" {
		return err
	}
	changed, err := captureReminders(tx, owner, a.Self().Fingerprint())
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.topicTitlesChanged()
	}
	return err
}

func (a *Agent) syncReminders() (bool, error) {
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
	seeded, err := captureReminders(tx, me.info.Person, fp)
	if err != nil {
		return false, err
	}
	more := false
	var copies []outCopy
	var sent [][]protocol.ReminderMark
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		if full, e := syncStateWindowFull(tx, dev); e != nil {
			return false, e
		} else if full {
			continue
		}
		r := protocol.ReminderSync{V: 1, Person: me.info.Person, Roster: me.info.Roster}
		if topicSyncAuthority(tx, protocol.TopicSync{Person: r.Person, Roster: r.Roster}, a.Address, fp, dev.Address, dev.Fingerprint()) != nil {
			continue
		}
		// A device that cannot read marks yet keeps one waiting carrier;
		// later marks stay unsealed here until it reads them, so nothing
		// piles up for an older program.
		var waiting int
		if err = tx.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND recipient_fp=? AND sub=? AND state=?`, dev.Address, dev.Fingerprint(), envelope.SubReminderSync, stateConvWaiting).Scan(&waiting); err != nil {
			return false, err
		}
		if waiting > 0 {
			continue
		}
		// A held carrier retains its exact facts; only a different mark
		// needs another copy.
		rows, e := tx.Query(`SELECT m.conv,m.fingerprint,m.lid,m.due,m.state,m.revision,m.writer FROM reminder_marks m WHERE m.owner=? AND NOT EXISTS(SELECT 1 FROM reminder_mark_copies c LEFT JOIN outbox o ON o.id=c.carrier WHERE c.owner=m.owner AND c.recipient_fp=? AND c.conv=m.conv AND c.fingerprint=m.fingerprint AND c.lid=m.lid AND c.due=m.due AND c.state=m.state AND c.revision=m.revision AND c.writer=m.writer AND (c.carrier='' OR o.state IN ('queued','waiting','custody','delivered','quarantined'))) ORDER BY m.conv,m.fingerprint,m.lid LIMIT ?`, me.info.Person, dev.Fingerprint(), protocol.MaxReminderMarks)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var m protocol.ReminderMark
			if e = rows.Scan(&m.Conv, &m.Fingerprint, &m.LID, &m.Due, &m.State, &m.Revision, &m.Writer); e != nil {
				break
			}
			r.Reminders = append(r.Reminders, m)
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if rowErr != nil {
			return false, rowErr
		}
		if len(r.Reminders) == 0 {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, e := dev.Recipient()
		if e != nil {
			return false, e
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubReminderSync, Replica: true, Body: string(raw)}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return false, e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapReminderSync, recipientFP: dev.Fingerprint()})
		sent = append(sent, r.Reminders)
		more = more || len(r.Reminders) == protocol.MaxReminderMarks
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
			if _, err = tx.Exec(`INSERT OR REPLACE INTO reminder_mark_copies(owner,recipient_fp,conv,fingerprint,lid,due,state,revision,writer,carrier) VALUES(?,?,?,?,?,?,?,?,?,?)`, me.info.Person, c.recipientFP, m.Conv, m.Fingerprint, m.LID, m.Due, m.State, m.Revision, m.Writer, c.env.ID); err != nil {
				return false, err
			}
		}
	}
	return more, a.store.done(tx.Commit())
}

func (a *Agent) admitReminderSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, err := protocol.ParseReminderSync([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person {
		return hold(reasonInvalid, "reminders belong to another person")
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
	if _, err = captureReminders(tx, r.Person, a.Self().Fingerprint()); err != nil {
		return err
	}
	for _, m := range r.Reminders {
		if _, err = saveReminderMark(tx, r.Person, m); err != nil {
			if errors.Is(err, errReminderGenerationConflict) {
				tx.Rollback()
				return hold(reasonInvalid, err.Error())
			}
			return err
		}
		// The sender has this mark: it needs no copy of it back.
		had, found, e := reminderMarkIn(tx, `reminder_mark_copies WHERE owner=? AND recipient_fp=? AND conv=? AND fingerprint=? AND lid=?`, r.Person, sender.Fingerprint(), m.Conv, m.Fingerprint, m.LID)
		if e != nil {
			return e
		}
		if !found || m.Newer(had) {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO reminder_mark_copies(owner,recipient_fp,conv,fingerprint,lid,due,state,revision,writer,carrier) VALUES(?,?,?,?,?,?,?,?,?,'')`, r.Person, sender.Fingerprint(), m.Conv, m.Fingerprint, m.LID, m.Due, m.State, m.Revision, m.Writer); err != nil {
				return err
			}
		}
	}
	if _, err = projectReminders(tx, r.Person, a.Self().Fingerprint()); err != nil {
		return err
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

func (a *Agent) mayDeliverReminderSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubReminderSync {
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
	r, parseErr := protocol.ParseReminderSync([]byte(body))
	auth := protocol.TopicSync{Person: r.Person, Roster: r.Roster}
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parseErr == nil && found && pending == nil && key.Fingerprint() == fp && topicSyncAuthority(a.store.db, auth, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "reminder owner or device authority changed", "")
		}
		return key, ok, e
	}
	key, ok, err := check()
	if err != nil || !ok {
		return true, false, err
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapReminderSync); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	// Capability lookup performs I/O; recheck exact current authority afterward.
	_, ok, err = check()
	return true, ok, err
}
