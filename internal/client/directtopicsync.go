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

func (a *Agent) syncDirectTopics() (bool, error) {
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
	owner, err := a.ownTopicWriter(tx)
	if err != nil || owner == "" {
		return false, err
	}
	me, _, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil {
		return false, err
	}
	var copies []outCopy
	more := false
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		if full, e := syncStateWindowFull(tx, dev); e != nil {
			return false, e
		} else if full {
			continue
		}
		r := protocol.DirectTopicSync{V: 1, Person: owner, Roster: me.info.Roster}
		if topicSyncAuthority(tx, protocol.TopicSync{Person: owner, Roster: r.Roster}, a.Address, a.Self().Fingerprint(), dev.Address, dev.Fingerprint()) != nil {
			continue
		}
		var waiting int
		if err = tx.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND recipient_fp=? AND sub=? AND state=?`, dev.Address, dev.Fingerprint(), envelope.SubDirectTopicSync, stateConvWaiting).Scan(&waiting); err != nil {
			return false, err
		}
		if waiting > 0 {
			continue
		}
		rows, e := tx.Query(`SELECT p.peer,p.id,p.author,p.topic,p.revision,p.writer,p.synthetic,p.host,p.host_key,p.agent_id FROM direct_topic_placements p WHERE owner=? AND NOT EXISTS(SELECT 1 FROM direct_topic_copies c LEFT JOIN outbox o ON o.id=c.carrier WHERE c.owner=p.owner AND c.recipient_fp=? AND c.peer=p.peer AND c.id=p.id AND c.author=p.author AND c.topic=p.topic AND c.revision=p.revision AND c.writer=p.writer AND c.synthetic=p.synthetic AND c.host=p.host AND c.host_key=p.host_key AND c.agent_id=p.agent_id AND (c.carrier='' OR o.state IN ('queued','waiting','custody','delivered','quarantined'))) ORDER BY p.peer,p.author,p.id LIMIT ?`, owner, dev.Fingerprint(), protocol.MaxTopicMarks)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var p protocol.DirectTopicPlacement
			if e = rows.Scan(&p.Peer, &p.ID, &p.Author, &p.Topic, &p.Revision, &p.Writer, &p.Synthetic, &p.Destination.Host, &p.Destination.Fingerprint, &p.Destination.AgentID); e != nil {
				break
			}
			r.Placements = append(r.Placements, p)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return false, e
		}
		if len(r.Placements) == 0 {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, e := dev.Recipient()
		if e != nil {
			return false, e
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubDirectTopicSync, Replica: true, Body: string(raw)}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return false, e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapDirectTopicSync, recipientFP: dev.Fingerprint()})
		more = more || len(r.Placements) == protocol.MaxTopicMarks
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
	for _, copy := range copies {
		r, _ := protocol.ParseDirectTopicSync([]byte(copy.in.Body))
		for _, p := range r.Placements {
			if err = saveDirectTopicCopy(tx, owner, copy.recipientFP, p, copy.env.ID); err != nil {
				return false, err
			}
		}
	}
	return more, a.store.done(tx.Commit())
}

func saveDirectTopicCopy(tx *sql.Tx, owner, recipient string, p protocol.DirectTopicPlacement, carrier string) error {
	_, err := tx.Exec(`INSERT INTO direct_topic_copies(owner,recipient_fp,peer,id,author,topic,revision,writer,synthetic,host,host_key,agent_id,carrier) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(owner,recipient_fp,author,id) DO UPDATE SET peer=excluded.peer,topic=excluded.topic,revision=excluded.revision,writer=excluded.writer,synthetic=excluded.synthetic,host=excluded.host,host_key=excluded.host_key,agent_id=excluded.agent_id,carrier=excluded.carrier WHERE (excluded.revision,excluded.writer,excluded.topic,excluded.peer,excluded.synthetic,excluded.host,excluded.host_key,excluded.agent_id)>=(direct_topic_copies.revision,direct_topic_copies.writer,direct_topic_copies.topic,direct_topic_copies.peer,direct_topic_copies.synthetic,direct_topic_copies.host,direct_topic_copies.host_key,direct_topic_copies.agent_id)`, owner, recipient, p.Peer, p.ID, p.Author, p.Topic, p.Revision, p.Writer, p.Synthetic, p.Destination.Host, p.Destination.Fingerprint, p.Destination.AgentID, carrier)
	return err
}

func (a *Agent) admitDirectTopicSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, err := protocol.ParseDirectTopicSync([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person {
		return hold(reasonInvalid, "direct topics belong to another person")
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
	for _, p := range r.Placements {
		if err = saveDirectTopicPlacement(tx, r.Person, p); err != nil {
			return err
		}
		if err = saveDirectTopicCopy(tx, r.Person, sender.Fingerprint(), p, ""); err != nil {
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

func (a *Agent) mayDeliverDirectTopicSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubDirectTopicSync {
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
	r, parseErr := protocol.ParseDirectTopicSync([]byte(body))
	auth := protocol.TopicSync{Person: r.Person, Roster: r.Roster}
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parseErr == nil && found && pending == nil && key.Fingerprint() == fp && topicSyncAuthority(a.store.db, auth, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "direct topic owner or device authority changed", "")
		}
		return key, ok, e
	}
	key, ok, err := check()
	if err != nil || !ok {
		return true, false, err
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapDirectTopicSync); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	_, ok, err = check()
	return true, ok, err
}
