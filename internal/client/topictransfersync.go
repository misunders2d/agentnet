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

func topicLocationIn(q dbq, table string, args ...any) (protocol.TopicLocation, bool, error) {
	var p protocol.TopicLocation
	var raw string
	e := q.QueryRow(`SELECT fact FROM `+table, args...).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return p, false, nil
	}
	if e == nil {
		e = json.Unmarshal([]byte(raw), &p)
	}
	return p, e == nil, e
}
func saveTopicLocation(tx *sql.Tx, owner string, p protocol.TopicLocation) error {
	old, ok, e := topicLocationIn(tx, `topic_locations WHERE owner=? AND scope=? AND topic=?`, owner, p.Scope, p.Topic)
	if e != nil || ok && !p.Newer(old) {
		return e
	}
	raw, _ := json.Marshal(p)
	_, e = tx.Exec(`INSERT INTO topic_locations(owner,scope,topic,fact) VALUES(?,?,?,?) ON CONFLICT(owner,scope,topic) DO UPDATE SET fact=excluded.fact`, owner, p.Scope, p.Topic, string(raw))
	return e
}
func (a *Agent) recordTopicLocation(tx *sql.Tx, r TopicTransferReview) error {
	owner, e := a.ownTopicWriter(tx)
	if e != nil {
		return e
	}
	if owner == "" {
		return errors.New("only a current own human device may record the private topic location")
	}
	scope, topic := transferScope(r.TopicTransferRequest)
	old, _, e := topicLocationIn(tx, `topic_locations WHERE owner=? AND scope=? AND topic=?`, owner, scope, topic)
	if e != nil {
		return e
	}
	p := protocol.TopicLocation{Scope: scope, Topic: topic, Destination: r.Destination, DestinationTopic: r.DestinationTopic, Operation: r.ID, At: time.Now().Unix(), Writer: a.Self().Fingerprint(), Revision: old.Revision + 1}
	if !p.Valid() {
		return errors.New("private topic location revision limit reached")
	}
	return saveTopicLocation(tx, owner, p)
}

type TopicTransferLocation struct {
	protocol.TopicLocation
	Available bool `json:"available"`
}

func (a *Agent) TopicTransferLocation(r TopicTransferRequest) (*TopicTransferLocation, error) {
	if !validTransferScope(r) {
		return nil, ErrTopicQuery
	}
	owner, e := a.ownTopicWriter(a.store.db)
	if e != nil || owner == "" {
		return nil, e
	}
	scope, topic := transferScope(r)
	p, ok, e := topicLocationIn(a.store.db, `topic_locations WHERE owner=? AND scope=? AND topic=?`, owner, scope, topic)
	if e != nil || !ok {
		return nil, e
	}
	return &TopicTransferLocation{TopicLocation: p, Available: topicOrganizationAuthor(a.store.db, p.Destination, a.Address, a.Self().Fingerprint()) == nil}, nil
}
func saveTopicLocationCopy(tx *sql.Tx, owner, fp string, p protocol.TopicLocation, carrier string) error {
	raw, _ := json.Marshal(p)
	_, e := tx.Exec(`INSERT INTO topic_location_copies(owner,recipient_fp,scope,topic,fact,carrier) VALUES(?,?,?,?,?,?) ON CONFLICT(owner,recipient_fp,scope,topic) DO UPDATE SET fact=excluded.fact,carrier=excluded.carrier`, owner, fp, p.Scope, p.Topic, string(raw), carrier)
	return e
}
func (a *Agent) syncTopicLocations() (bool, error) {
	release, e := lockfile.Wait(a.spoolLockPath())
	if e != nil {
		return false, e
	}
	defer release()
	tx, e := a.store.db.Begin()
	if e != nil {
		return false, e
	}
	defer tx.Rollback()
	me, ok, e := scanPersonIn(tx, "state = ?", personSelf)
	if e != nil || !ok {
		return false, e
	}
	fp := a.Self().Fingerprint()
	if !me.has(a.Address, fp) || !me.roster.Human(fp) {
		return false, nil
	}
	var copies []outCopy
	var sent [][]protocol.TopicLocation
	more := false
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		if full, x := syncStateWindowFull(tx, dev); x != nil {
			return false, x
		} else if full {
			continue
		}
		auth := protocol.TopicSync{Person: me.info.Person, Roster: me.info.Roster}
		if topicSyncAuthority(tx, auth, a.Address, fp, dev.Address, dev.Fingerprint()) != nil {
			continue
		}
		var waiting int
		if e = tx.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND recipient_fp=? AND sub=? AND required_cap=? AND state=?`, dev.Address, dev.Fingerprint(), envelope.SubTopicStateSync, protocol.CapTopicStateSyncV2, stateConvWaiting).Scan(&waiting); e != nil {
			return false, e
		}
		if waiting > 0 {
			continue
		}
		rows, x := tx.Query(`SELECT l.fact FROM topic_locations l WHERE l.owner=? AND NOT EXISTS(SELECT 1 FROM topic_location_copies c LEFT JOIN outbox o ON o.id=c.carrier WHERE c.owner=l.owner AND c.recipient_fp=? AND c.scope=l.scope AND c.topic=l.topic AND c.fact=l.fact AND (c.carrier='' OR o.state IN ('queued','waiting','custody','delivered','quarantined'))) ORDER BY l.scope,l.topic LIMIT ?`, me.info.Person, dev.Fingerprint(), protocol.MaxTopicMarks)
		if x != nil {
			return false, x
		}
		r := protocol.TopicLocationSync{V: 2, Person: me.info.Person, Roster: me.info.Roster}
		for rows.Next() {
			var raw string
			var p protocol.TopicLocation
			if x = rows.Scan(&raw); x != nil {
				break
			}
			if x = json.Unmarshal([]byte(raw), &p); x != nil {
				break
			}
			r.Locations = append(r.Locations, p)
		}
		if x == nil {
			x = rows.Err()
		}
		rows.Close()
		if x != nil {
			return false, x
		}
		if len(r.Locations) == 0 {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, x := dev.Recipient()
		if x != nil {
			return false, x
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubTopicStateSync, Replica: true, Body: string(raw)}
		env, x := envelope.Seal(in, a.id.Sign, recipient)
		if x != nil {
			return false, x
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapTopicStateSyncV2, recipientFP: dev.Fingerprint()})
		sent = append(sent, r.Locations)
		more = more || len(r.Locations) == protocol.MaxTopicMarks
		if len(copies) == historyPage {
			more = true
			break
		}
	}
	if len(copies) == 0 {
		return false, nil
	}
	if e = insertCopies(tx, copies); e != nil {
		return false, e
	}
	for i, c := range copies {
		for _, p := range sent[i] {
			if e = saveTopicLocationCopy(tx, me.info.Person, c.recipientFP, p, c.env.ID); e != nil {
				return false, e
			}
		}
	}
	return more, a.store.done(tx.Commit())
}
func (a *Agent) admitTopicLocationSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, e := protocol.ParseTopicLocationSync([]byte(in.Body))
	if e != nil {
		return hold(reasonInvalid, e.Error())
	}
	me, ok, e := a.store.selfPerson(a.Address)
	if e != nil {
		return e
	}
	if !ok || r.Person != me.info.Person {
		return hold(reasonInvalid, "topic locations belong to another person")
	}
	if e = a.refreshRecipientPerson(ctx, env.From, map[string]error{}); e != nil {
		return e
	}
	tx, e := a.store.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if e = topicSyncAuthority(tx, protocol.TopicSync{Person: r.Person, Roster: r.Roster}, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); e != nil {
		tx.Rollback()
		return hold(reasonInvalid, e.Error())
	}
	for _, p := range r.Locations {
		if e = saveTopicLocation(tx, r.Person, p); e != nil {
			return e
		}
		old, ok, x := topicLocationIn(tx, `topic_location_copies WHERE owner=? AND recipient_fp=? AND scope=? AND topic=?`, r.Person, sender.Fingerprint(), p.Scope, p.Topic)
		if x != nil {
			return x
		}
		if !ok || p.Newer(old) {
			if e = saveTopicLocationCopy(tx, r.Person, sender.Fingerprint(), p, ""); e != nil {
				return e
			}
		}
	}
	if held {
		if _, e = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); e != nil {
			return e
		}
	}
	if e = receiptCarrier(tx, env.ID); e != nil {
		return e
	}
	if e = a.store.done(tx.Commit()); e == nil {
		a.convWork.due(convHistory)
		a.kickNow()
	}
	return e
}
func topicStateV2(body string) bool {
	var v struct {
		V int `json:"v"`
	}
	return json.Unmarshal([]byte(body), &v) == nil && v.V == 2
}
func (a *Agent) mayDeliverTopicLocationSync(env envelope.Envelope, body, fp string) (bool, bool, error) {
	if e := a.refreshRecipientPerson(context.Background(), env.To, map[string]error{}); e != nil {
		return true, false, e
	}
	r, parseErr := protocol.ParseTopicLocationSync([]byte(body))
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parseErr == nil && found && pending == nil && key.Fingerprint() == fp && topicSyncAuthority(a.store.db, protocol.TopicSync{Person: r.Person, Roster: r.Roster}, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "topic location owner or device authority changed", "")
		}
		return key, ok, e
	}
	key, ok, e := check()
	if e != nil || !ok {
		return true, false, e
	}
	if e = a.requireParticipationCaps(context.Background(), key, protocol.CapTopicStateSyncV2); e != nil {
		if errors.Is(e, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+e.Error(), "")
		}
		return true, false, e
	}
	_, ok, e = check()
	return true, ok, e
}
