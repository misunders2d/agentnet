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

// Latest private titles survive missing history and resets. Copy markers are
// bounded by scope/topic/recipient, rather than one row per rename.
const topicSyncSchema = `
CREATE TABLE topic_titles(owner TEXT NOT NULL, scope TEXT NOT NULL, topic TEXT NOT NULL, title TEXT NOT NULL, revision INTEGER NOT NULL, writer TEXT NOT NULL, PRIMARY KEY(owner,scope,topic));
CREATE TABLE topic_title_copies(owner TEXT NOT NULL, recipient_fp TEXT NOT NULL, scope TEXT NOT NULL, topic TEXT NOT NULL, revision INTEGER NOT NULL, writer TEXT NOT NULL, carrier TEXT NOT NULL, PRIMARY KEY(owner,recipient_fp,scope,topic));
`

func topicSyncAuthority(q dbq, r protocol.TopicSync, from, fromFP, to, toFP string) error {
	return readSyncAuthority(q, protocol.ReadSync{Person: r.Person, Roster: r.Roster}, from, fromFP, to, toFP)
}

var errTopicTitleConflict = errors.New("topic sync: conflicting exact revision")

func saveTopicTitle(tx *sql.Tx, owner string, title protocol.TopicTitle) error {
	var old protocol.TopicTitle
	err := tx.QueryRow(`SELECT title,revision,writer FROM topic_titles WHERE owner=? AND scope=? AND topic=?`, owner, title.Scope, title.Topic).Scan(&old.Title, &old.Rev, &old.Writer)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		if title.Rev < old.Rev || title.Rev == old.Rev && title.Writer < old.Writer {
			return nil
		}
		if title.Rev == old.Rev && title.Writer == old.Writer {
			if title.Title != old.Title {
				return errTopicTitleConflict
			}
			return nil
		}
	}
	if _, err = tx.Exec(`INSERT INTO topic_titles(owner,scope,topic,title,revision,writer) VALUES(?,?,?,?,?,?) ON CONFLICT(owner,scope,topic) DO UPDATE SET title=excluded.title,revision=excluded.revision,writer=excluded.writer`, owner, title.Scope, title.Topic, title.Title, title.Rev, title.Writer); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO topic_state(peer,topic,title,updated_at) VALUES(?,?,?,?) ON CONFLICT(peer,topic) DO UPDATE SET title=excluded.title,updated_at=excluded.updated_at`, title.Scope, title.Topic, title.Title, storeNow().Unix())
	return err
}

// Record only explicit title changes. Reading a topic or marking it done cannot
// reset a title; a received preference does not create a new local revision.
func (a *Agent) recordTopicTitle(tx *sql.Tx, scope, topic, title string) error {
	me, ok, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil || !ok {
		return err
	}
	fp := a.Self().Fingerprint()
	if !me.has(a.Address, fp) || !me.roster.Human(fp) {
		return nil
	}
	var rev int64
	if err = tx.QueryRow(`SELECT coalesce(max(revision),0) FROM topic_titles WHERE owner=? AND scope=? AND topic=?`, me.info.Person, scope, topic).Scan(&rev); err != nil {
		return err
	}
	if rev == protocol.MaxTopicTitleRevision {
		return errors.New("topic sync: revision limit reached")
	}
	// Revision one is reserved for pre-upgrade names. Such a late seed must
	// never replace an explicit new rename or reset from another device.
	return saveTopicTitle(tx, me.info.Person, protocol.TopicTitle{Scope: scope, Topic: topic, Title: title, Rev: max(rev, 1) + 1, Writer: fp})
}

func (a *Agent) setTopicTitle(scope, topic, title string) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO topic_state(peer,topic,title,updated_at) VALUES(?,?,?,?) ON CONFLICT(peer,topic) DO UPDATE SET title=excluded.title,updated_at=excluded.updated_at`, scope, topic, title, storeNow().Unix()); err != nil {
		return err
	}
	if err = a.recordTopicTitle(tx, scope, topic, title); err != nil {
		return err
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.topicTitlesChanged()
	}
	return err
}

func (a *Agent) topicTitlesChanged() {
	a.convWork.due(convHistory)
	a.kickNow()
	notifyDaemon(a.home)
}

func (a *Agent) syncTopicTitles() (bool, error) {
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
	// Preserve pre-upgrade custom names, but absence of a local name is never
	// a reset. A preference received before this migration always wins.
	res, err := tx.Exec(`INSERT INTO topic_titles(owner,scope,topic,title,revision,writer) SELECT ?,peer,topic,title,1,? FROM topic_state s WHERE coalesce(title,'')!='' AND NOT EXISTS(SELECT 1 FROM topic_titles t WHERE t.owner=? AND t.scope=s.peer AND t.topic=s.topic) LIMIT ?`, me.info.Person, fp, me.info.Person, protocol.MaxTopicTitles)
	if err != nil {
		return false, err
	}
	migrated, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	more := migrated == protocol.MaxTopicTitles
	var copies []outCopy
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		if full, e := syncWindowFull(tx, dev); e != nil {
			return false, e
		} else if full {
			continue
		}
		r := protocol.TopicSync{V: 1, Person: me.info.Person, Roster: me.info.Roster}
		if topicSyncAuthority(tx, r, a.Address, fp, dev.Address, dev.Fingerprint()) != nil {
			continue
		}
		// A held carrier retains its exact facts. Only a new revision/writer
		// needs another copy; a full quarantined batch must not self-reschedule.
		rows, e := tx.Query(`SELECT t.scope,t.topic,t.title,t.revision,t.writer FROM topic_titles t WHERE t.owner=? AND NOT EXISTS(SELECT 1 FROM topic_title_copies c JOIN outbox o ON o.id=c.carrier WHERE c.owner=t.owner AND c.recipient_fp=? AND c.scope=t.scope AND c.topic=t.topic AND c.revision=t.revision AND c.writer=t.writer AND o.state IN ('queued','waiting','custody','delivered','quarantined')) ORDER BY t.scope,t.topic LIMIT ?`, me.info.Person, dev.Fingerprint(), protocol.MaxTopicTitles)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var title protocol.TopicTitle
			if e = rows.Scan(&title.Scope, &title.Topic, &title.Title, &title.Rev, &title.Writer); e != nil {
				break
			}
			r.Titles = append(r.Titles, title)
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if rowErr != nil {
			return false, rowErr
		}
		if len(r.Titles) == 0 {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, e := dev.Recipient()
		if e != nil {
			return false, e
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubTopicSync, Replica: true, Body: string(raw)}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return false, e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapOwnSyncV2, recipientFP: dev.Fingerprint()})
		more = more || len(r.Titles) == protocol.MaxTopicTitles
		if len(copies) == historyPage {
			more = true
			break
		}
	}
	if len(copies) == 0 && migrated == 0 {
		return false, nil
	}
	if err = insertCopies(tx, copies); err != nil {
		return false, err
	}
	for _, c := range copies {
		r, _ := protocol.ParseTopicSync([]byte(c.in.Body))
		for _, title := range r.Titles {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO topic_title_copies(owner,recipient_fp,scope,topic,revision,writer,carrier) VALUES(?,?,?,?,?,?,?)`, r.Person, c.recipientFP, title.Scope, title.Topic, title.Rev, title.Writer, c.env.ID); err != nil {
				return false, err
			}
		}
	}
	return more, a.store.done(tx.Commit())
}

func (a *Agent) admitTopicSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, err := protocol.ParseTopicSync([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person {
		return hold(reasonInvalid, "topic sync belongs to another person")
	}
	if err = a.refreshRecipientPerson(ctx, env.From, map[string]error{}); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = topicSyncAuthority(tx, r, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	for _, title := range r.Titles {
		if err = saveTopicTitle(tx, r.Person, title); err != nil {
			if errors.Is(err, errTopicTitleConflict) {
				tx.Rollback()
				return hold(reasonInvalid, err.Error())
			}
			return err
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

func (a *Agent) mayDeliverTopicSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubTopicSync {
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
	r, parseErr := protocol.ParseTopicSync([]byte(body))
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parseErr == nil && found && pending == nil && key.Fingerprint() == fp && topicSyncAuthority(a.store.db, r, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "topic title owner or device authority changed", "")
		}
		return key, ok, e
	}
	key, ok, err := check()
	if err != nil || !ok {
		return true, false, err
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapOwnSyncV2); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	// Capability lookup performs I/O; recheck exact current authority afterward.
	_, ok, err = check()
	return true, ok, err
}
