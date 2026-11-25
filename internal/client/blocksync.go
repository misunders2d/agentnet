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

// Blocking a person (Google Play user-generated content policy; owner
// decision, docs/DECISIONS.md November 25, 2026) is private to the owner and
// covers direct conversations only: that person's direct messages, questions
// and tasks are held quietly on every one of the owner's devices. Groups are
// unchanged. Marks travel only between current own-human devices, like
// reminder marks (remindersync.go); the newest revision wins.
const blockSyncSchema = `
CREATE TABLE person_blocks(owner TEXT NOT NULL,person TEXT NOT NULL,blocked INTEGER NOT NULL,revision INTEGER NOT NULL,writer TEXT NOT NULL,PRIMARY KEY(owner,person));
CREATE TABLE person_block_copies(owner TEXT NOT NULL,recipient_fp TEXT NOT NULL,person TEXT NOT NULL,blocked INTEGER NOT NULL,revision INTEGER NOT NULL,writer TEXT NOT NULL,carrier TEXT NOT NULL,PRIMARY KEY(owner,recipient_fp,person));
`

// reasonBlocked holds a direct message from a person the owner blocked. It is
// never offered for review, counted unread or alerted.
const reasonBlocked = "blocked"

var errBlockSelf = errors.New("you cannot block yourself")

func blockMarkIn(q dbq, from string, args ...any) (protocol.BlockMark, bool, error) {
	var m protocol.BlockMark
	err := q.QueryRow(`SELECT person,blocked,revision,writer FROM `+from, args...).Scan(&m.Person, &m.Blocked, &m.Revision, &m.Writer)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	return m, err == nil, err
}

func saveBlockMark(tx *sql.Tx, owner string, m protocol.BlockMark) (bool, error) {
	old, found, err := blockMarkIn(tx, `person_blocks WHERE owner=? AND person=?`, owner, m.Person)
	if err != nil {
		return false, err
	}
	if found && !m.Newer(old) {
		return false, nil
	}
	_, err = tx.Exec(`INSERT INTO person_blocks(owner,person,blocked,revision,writer) VALUES(?,?,?,?,?) ON CONFLICT(owner,person) DO UPDATE SET blocked=excluded.blocked,revision=excluded.revision,writer=excluded.writer`, owner, m.Person, m.Blocked, m.Revision, m.Writer)
	return err == nil, err
}

// personBlocked reports whether owner blocked person.
func personBlocked(q dbq, owner, person string) (bool, error) {
	m, found, err := blockMarkIn(q, `person_blocks WHERE owner=? AND person=?`, owner, person)
	return found && m.Blocked, err
}

// SetBlocked blocks or unblocks person for this installation's person, on
// all of their devices once synchronized.
func (a *Agent) SetBlocked(person string, blocked bool) error {
	if !protocol.ValidID(person) {
		return errors.New("block: not a person")
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	me, ok, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("block: this installation has no person")
	}
	if person == me.info.Person {
		return errBlockSelf
	}
	old, _, err := blockMarkIn(tx, `person_blocks WHERE owner=? AND person=?`, me.info.Person, person)
	if err != nil {
		return err
	}
	m := protocol.BlockMark{Person: person, Blocked: blocked, Revision: max(time.Now().UnixMilli(), old.Revision+1), Writer: a.Self().Fingerprint()}
	if _, err = saveBlockMark(tx, me.info.Person, m); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.convWork.due(convHistory)
	a.kickNow()
	return nil
}

// BlockedPersons lists the persons this installation's person blocked.
func (a *Agent) BlockedPersons() ([]string, error) {
	rows, err := a.store.db.Query(`SELECT b.person FROM person_blocks b JOIN persons p ON p.person=b.owner AND p.state=? WHERE b.blocked=1 ORDER BY b.person`, personSelf)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (a *Agent) syncBlocks() (bool, error) {
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
	more := false
	var copies []outCopy
	var sent [][]protocol.BlockMark
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		if full, e := syncStateWindowFull(tx, dev); e != nil {
			return false, e
		} else if full {
			continue
		}
		r := protocol.BlockSync{V: 1, Person: me.info.Person, Roster: me.info.Roster}
		if topicSyncAuthority(tx, protocol.TopicSync{Person: r.Person, Roster: r.Roster}, a.Address, fp, dev.Address, dev.Fingerprint()) != nil {
			continue
		}
		// A device that cannot read marks yet keeps one waiting carrier.
		var waiting int
		if err = tx.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND recipient_fp=? AND sub=? AND state=?`, dev.Address, dev.Fingerprint(), envelope.SubBlockSync, stateConvWaiting).Scan(&waiting); err != nil {
			return false, err
		}
		if waiting > 0 {
			continue
		}
		rows, e := tx.Query(`SELECT m.person,m.blocked,m.revision,m.writer FROM person_blocks m WHERE m.owner=? AND NOT EXISTS(SELECT 1 FROM person_block_copies c LEFT JOIN outbox o ON o.id=c.carrier WHERE c.owner=m.owner AND c.recipient_fp=? AND c.person=m.person AND c.blocked=m.blocked AND c.revision=m.revision AND c.writer=m.writer AND (c.carrier='' OR o.state IN ('queued','waiting','custody','delivered','quarantined'))) ORDER BY m.person LIMIT ?`, me.info.Person, dev.Fingerprint(), protocol.MaxBlockMarks)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var m protocol.BlockMark
			if e = rows.Scan(&m.Person, &m.Blocked, &m.Revision, &m.Writer); e != nil {
				break
			}
			r.Blocks = append(r.Blocks, m)
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if rowErr != nil {
			return false, rowErr
		}
		if len(r.Blocks) == 0 {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, e := dev.Recipient()
		if e != nil {
			return false, e
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubBlockSync, Replica: true, Body: string(raw)}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return false, e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapReminderSyncV2, recipientFP: dev.Fingerprint()})
		sent = append(sent, r.Blocks)
		more = more || len(r.Blocks) == protocol.MaxBlockMarks
		if len(copies) == historyPage {
			more = true
			break
		}
	}
	if len(copies) == 0 {
		return false, nil
	}
	if err = insertCopies(tx, copies); err != nil {
		return false, err
	}
	for i, c := range copies {
		for _, m := range sent[i] {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO person_block_copies(owner,recipient_fp,person,blocked,revision,writer,carrier) VALUES(?,?,?,?,?,?,?)`, me.info.Person, c.recipientFP, m.Person, m.Blocked, m.Revision, m.Writer, c.env.ID); err != nil {
				return false, err
			}
		}
	}
	return more, a.store.done(tx.Commit())
}

func (a *Agent) admitBlockSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, err := protocol.ParseBlockSync([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person {
		return hold(reasonInvalid, "blocks belong to another person")
	}
	if err = a.refreshRecipientPerson(ctx, env.From, map[string]error{}); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = topicSyncAuthority(tx, protocol.TopicSync{Person: r.Person, Roster: r.Roster}, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	for _, m := range r.Blocks {
		if _, err = saveBlockMark(tx, r.Person, m); err != nil {
			return err
		}
		// The sender has this mark: it needs no copy of it back.
		had, found, e := blockMarkIn(tx, `person_block_copies WHERE owner=? AND recipient_fp=? AND person=?`, r.Person, sender.Fingerprint(), m.Person)
		if e != nil {
			return e
		}
		if !found || m.Newer(had) {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO person_block_copies(owner,recipient_fp,person,blocked,revision,writer,carrier) VALUES(?,?,?,?,?,?,'')`, r.Person, sender.Fingerprint(), m.Person, m.Blocked, m.Revision, m.Writer); err != nil {
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

func (a *Agent) mayDeliverBlockSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubBlockSync {
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
	r, parseErr := protocol.ParseBlockSync([]byte(body))
	auth := protocol.TopicSync{Person: r.Person, Roster: r.Roster}
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parseErr == nil && found && pending == nil && key.Fingerprint() == fp && topicSyncAuthority(a.store.db, auth, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "block owner or device authority changed", "")
		}
		return key, ok, e
	}
	key, ok, err := check()
	if err != nil || !ok {
		return true, false, err
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapReminderSyncV2); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	_, ok, err = check()
	return true, ok, err
}

// holdBlocked keeps a direct message from a blocked person quietly: held,
// acknowledged, never offered for review, counted unread or alerted, and not
// released by unblocking (as Telegram and WhatsApp drop what a blocked
// contact sent).
func (a *Agent) holdBlocked(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public) error {
	if err := a.holdOpened(ctx, "conversation message", env, in, sender, reasonBlocked, "sender is blocked"); err != nil {
		return err
	}
	_, err := a.store.db.Exec(`UPDATE quarantine SET notice_archived=1 WHERE id=? AND reason=?`, env.ID, reasonBlocked)
	return err
}
