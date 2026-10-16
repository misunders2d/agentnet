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

// Append-only exact references, scoped to the installation's verified owner.
// Capturing UPDATE read_at covers every existing local read entry point.
const readSyncSchema = `
CREATE INDEX inbox_read_ref ON inbox(coalesce(conv,''),coalesce(verified_by,claimed_fp),coalesce(nullif(lid,''),id)) WHERE local=0 AND ref_id IS NULL;
CREATE TABLE read_marks(owner TEXT NOT NULL, conv TEXT NOT NULL, fingerprint TEXT NOT NULL, lid TEXT NOT NULL,
 PRIMARY KEY(owner,conv,fingerprint,lid));
CREATE TABLE read_mark_copies(owner TEXT NOT NULL, recipient_fp TEXT NOT NULL, conv TEXT NOT NULL, fingerprint TEXT NOT NULL, lid TEXT NOT NULL, carrier TEXT NOT NULL,
 PRIMARY KEY(owner,recipient_fp,conv,fingerprint,lid));
INSERT OR IGNORE INTO read_marks SELECT p.person,coalesce(i.conv,''),coalesce(i.verified_by,i.claimed_fp),coalesce(nullif(i.lid,''),i.id) FROM inbox i JOIN persons p ON p.state='self' WHERE i.read_at IS NOT NULL AND i.local=0 AND i.ref_id IS NULL AND length(coalesce(i.verified_by,i.claimed_fp,''))=35 AND coalesce(i.sub,'') NOT IN ('read-sync','root-sync','drive-space','group-proof','group-context','group-invite','group-consent','group-withdrawal');
CREATE TRIGGER capture_read_owner AFTER INSERT ON persons WHEN NEW.state='self'
BEGIN
 INSERT OR IGNORE INTO read_marks SELECT NEW.person,coalesce(conv,''),coalesce(verified_by,claimed_fp),coalesce(nullif(lid,''),id) FROM inbox WHERE read_at IS NOT NULL AND local=0 AND ref_id IS NULL AND length(coalesce(verified_by,claimed_fp,''))=35 AND coalesce(sub,'') NOT IN ('read-sync','root-sync','drive-space','group-proof','group-context','group-invite','group-consent','group-withdrawal');
END;
CREATE TRIGGER capture_read_owner_state AFTER UPDATE OF state ON persons WHEN NEW.state='self' AND OLD.state!='self'
BEGIN
 INSERT OR IGNORE INTO read_marks SELECT NEW.person,coalesce(conv,''),coalesce(verified_by,claimed_fp),coalesce(nullif(lid,''),id) FROM inbox WHERE read_at IS NOT NULL AND local=0 AND ref_id IS NULL AND length(coalesce(verified_by,claimed_fp,''))=35 AND coalesce(sub,'') NOT IN ('read-sync','root-sync','drive-space','group-proof','group-context','group-invite','group-consent','group-withdrawal');
END;
CREATE TRIGGER capture_read AFTER UPDATE OF read_at ON inbox
WHEN NEW.read_at IS NOT NULL AND NEW.local=0 AND NEW.ref_id IS NULL AND length(coalesce(NEW.verified_by,NEW.claimed_fp,''))=35 AND coalesce(NEW.sub,'') NOT IN ('read-sync','root-sync','drive-space','group-proof','group-context','group-invite','group-consent','group-withdrawal')
BEGIN
 INSERT OR IGNORE INTO read_marks SELECT person,coalesce(NEW.conv,''),coalesce(NEW.verified_by,NEW.claimed_fp),coalesce(nullif(NEW.lid,''),NEW.id) FROM persons WHERE state='self';
END;
CREATE TRIGGER apply_read_mark AFTER INSERT ON read_marks
WHEN EXISTS(SELECT 1 FROM persons WHERE state='self' AND person=NEW.owner)
BEGIN
 UPDATE inbox SET read_at=coalesce(read_at,unixepoch()) WHERE local=0 AND ref_id IS NULL AND coalesce(conv,'')=NEW.conv AND coalesce(verified_by,claimed_fp)=NEW.fingerprint AND coalesce(nullif(lid,''),id)=NEW.lid;
END;
CREATE TRIGGER apply_read_arrival AFTER INSERT ON inbox
WHEN NEW.read_at IS NULL AND NEW.local=0 AND NEW.ref_id IS NULL
BEGIN
 UPDATE inbox SET read_at=unixepoch() WHERE id=NEW.id AND EXISTS(SELECT 1 FROM read_marks r JOIN persons p ON p.person=r.owner AND p.state='self' WHERE r.conv=coalesce(NEW.conv,'') AND r.fingerprint=coalesce(NEW.verified_by,NEW.claimed_fp) AND r.lid=coalesce(nullif(NEW.lid,''),NEW.id));
END;
CREATE TRIGGER apply_read_verified AFTER UPDATE OF verified_by,claimed_fp,conv,lid ON inbox
WHEN NEW.read_at IS NULL AND NEW.local=0 AND NEW.ref_id IS NULL
BEGIN
 UPDATE inbox SET read_at=unixepoch() WHERE id=NEW.id AND EXISTS(SELECT 1 FROM read_marks r JOIN persons p ON p.person=r.owner AND p.state='self' WHERE r.conv=coalesce(NEW.conv,'') AND r.fingerprint=coalesce(NEW.verified_by,NEW.claimed_fp) AND r.lid=coalesce(nullif(NEW.lid,''),NEW.id));
END;
`

func readSyncAuthority(q dbq, r protocol.ReadSync, from, fromFP, to, toFP string) error {
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person || from == to || !me.has(from, fromFP) || !me.has(to, toFP) || !me.roster.Human(fromFP) || !me.roster.Human(toFP) {
		return errors.New("read sync: only current own-human devices may share read state")
	}
	for _, endpoint := range []struct{ address, fp string }{{from, fromFP}, {to, toFP}} {
		if revoked, e := revokedDeviceKnown(q, endpoint.address, endpoint.fp); e != nil {
			return e
		} else if revoked {
			return errors.New("read sync: revoked device")
		}
		var raw string
		var pending sql.NullString
		e := q.QueryRow(`SELECT public,pending FROM peers WHERE address=?`, endpoint.address).Scan(&raw, &pending)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		var key identity.Public
		if pending.Valid || json.Unmarshal([]byte(raw), &key) != nil || key.Fingerprint() != endpoint.fp {
			return errors.New("read sync: device key changed or pending")
		}
	}
	bound, err := inChainIn(q, r.Person, r.Roster)
	if err != nil {
		return err
	}
	if !bound {
		return errors.New("read sync: unverified roster")
	}
	return nil
}

func (a *Agent) syncReadMarks() (bool, error) {
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return false, err
	}
	defer release()
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return false, err
	}
	if !me.has(a.Address, a.Self().Fingerprint()) || !me.roster.Human(a.Self().Fingerprint()) {
		return false, nil
	}
	var copies []outCopy
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		if full, e := syncWindowFull(a.store.db, dev); e != nil {
			return false, e
		} else if full {
			continue
		}
		key, pending, found, e := a.store.peer(dev.Address)
		if e != nil {
			return false, e
		}
		if pending != nil || found && key.Fingerprint() != dev.Fingerprint() {
			continue
		}
		// Keep one immutable attention carrier until this exact reader can
		// accept it. Later generations stay canonical; V1 refs still flow.
		var attentionPending bool
		if e = a.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM outbox WHERE recipient=? AND recipient_fp=? AND sub=? AND required_cap=? AND state IN ('queued','waiting'))`, dev.Address, dev.Fingerprint(), envelope.SubReadSync, protocol.CapReadSyncV2).Scan(&attentionPending); e != nil {
			return false, e
		}
		// Batch up to 64 exact references; indexed copy mappings point to existing outbox rows
		// are the reconciliation marker. Quarantine retains that exact encrypted
		// carrier for receiver recovery; resealing the same full batch would
		// make convSync immediately schedule it forever.
		rows, e := a.store.db.Query(`SELECT m.conv AS conv,m.fingerprint AS fingerprint,m.lid AS lid,m.state,m.revision,m.writer,0 AS priority FROM attention_marks m WHERE m.owner=? AND NOT ? AND NOT EXISTS(SELECT 1 FROM attention_mark_copies c JOIN outbox o ON o.id=c.carrier WHERE c.owner=m.owner AND c.recipient_fp=? AND c.conv=m.conv AND c.fingerprint=m.fingerprint AND c.lid=m.lid AND c.revision=m.revision AND c.writer=m.writer AND o.state IN ('queued','waiting','custody','delivered','quarantined'))
 UNION ALL SELECT r.conv,r.fingerprint,r.lid,'',0,'',1 FROM read_marks r WHERE r.owner=? AND NOT EXISTS(SELECT 1 FROM read_mark_copies c JOIN outbox o ON o.id=c.carrier WHERE c.owner=r.owner AND c.recipient_fp=? AND c.conv=r.conv AND c.fingerprint=r.fingerprint AND c.lid=r.lid AND o.state IN ('queued','waiting','custody','delivered','quarantined')) ORDER BY priority,conv,fingerprint,lid LIMIT ?`, me.info.Person, attentionPending, dev.Fingerprint(), me.info.Person, dev.Fingerprint(), protocol.MaxReadRefs)
		if e != nil {
			return false, e
		}
		var refs []protocol.ReadRef
		var marks []protocol.AttentionMark
		for rows.Next() {
			var m protocol.AttentionMark
			var priority int
			if e = rows.Scan(&m.Conv, &m.Fingerprint, &m.LID, &m.State, &m.Rev, &m.Writer, &priority); e != nil {
				break
			}
			if priority == 0 {
				marks = append(marks, m)
			} else {
				refs = append(refs, m.ReadRef)
			}
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if rowErr != nil {
			return false, rowErr
		}
		if len(refs) == 0 && len(marks) == 0 {
			continue
		}
		r := protocol.ReadSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Refs: refs}
		required := protocol.CapReadSync
		if len(marks) > 0 {
			r.V = 2
			r.Refs = nil
			r.Marks = marks
			required = protocol.CapReadSyncV2
		}
		if e = readSyncAuthority(a.store.db, r, a.Address, a.Self().Fingerprint(), dev.Address, dev.Fingerprint()); e != nil {
			continue
		}
		raw, _ := json.Marshal(r)
		recipient, e := dev.Recipient()
		if e != nil {
			return false, e
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return false, e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: required, recipientFP: dev.Fingerprint()})
		if len(copies) == historyPage {
			break
		}
	}
	if len(copies) == 0 {
		return false, nil
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	for _, c := range copies {
		r, e := protocol.ParseReadSync([]byte(c.in.Body))
		if e != nil {
			return false, e
		}
		if e = readSyncAuthority(tx, r, a.Address, a.Self().Fingerprint(), c.in.To, c.recipientFP); e != nil {
			return false, e
		}
	}
	if err = insertCopies(tx, copies); err != nil {
		return false, err
	}
	more := false
	for _, c := range copies {
		r, _ := protocol.ParseReadSync([]byte(c.in.Body))
		if len(r.Refs) == protocol.MaxReadRefs || len(r.Marks) > 0 {
			more = true
		}
		for _, ref := range r.Refs {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO read_mark_copies(owner,recipient_fp,conv,fingerprint,lid,carrier) VALUES(?,?,?,?,?,?)`, r.Person, c.recipientFP, ref.Conv, ref.Fingerprint, ref.LID, c.env.ID); err != nil {
				return false, err
			}
		}
		for _, m := range r.Marks {
			if _, err = tx.Exec(`INSERT INTO attention_mark_copies(owner,recipient_fp,conv,fingerprint,lid,revision,writer,carrier) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(owner,recipient_fp,conv,fingerprint,lid) DO UPDATE SET revision=excluded.revision,writer=excluded.writer,carrier=excluded.carrier`, r.Person, c.recipientFP, m.Conv, m.Fingerprint, m.LID, m.Rev, m.Writer, c.env.ID); err != nil {
				return false, err
			}
		}

	}
	err = a.store.done(tx.Commit())
	return more || len(copies) == historyPage, err
}

func (a *Agent) admitReadSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, err := protocol.ParseReadSync([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || r.Person != me.info.Person {
		return hold(reasonInvalid, "read sync belongs to another person")
	}
	if err = a.refreshRecipientPerson(ctx, env.From, map[string]error{}); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = readSyncAuthority(tx, r, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	for _, ref := range r.Refs {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO read_marks(owner,conv,fingerprint,lid) VALUES(?,?,?,?)`, r.Person, ref.Conv, ref.Fingerprint, ref.LID); err != nil {
			return err
		}
	}
	if err = saveAttentionMarks(tx, r.Person, r.Marks); err != nil {
		tx.Rollback()
		if errors.Is(err, errAttentionConflict) {
			return hold(reasonConflict, err.Error())
		}
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
	if err = a.store.done(tx.Commit()); err != nil {
		return err
	}
	a.convWork.due(convHistory)
	a.kickNow()
	return nil
}

func (a *Agent) mayDeliverReadSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubReadSync {
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
	r, err := protocol.ParseReadSync([]byte(body))
	if err == nil {
		err = readSyncAuthority(a.store.db, r, env.From, a.Self().Fingerprint(), env.To, fp)
	}
	key, pending, found, e := a.store.peer(env.To)
	if e != nil {
		return true, false, e
	}
	if err != nil || !found || pending != nil || key.Fingerprint() != fp {
		return true, false, a.store.setOutboxState(env.ID, stateNotDelivered, "read sync owner or device authority changed", "")
	}
	required := protocol.CapReadSync
	if len(r.Marks) > 0 {
		required = protocol.CapReadSyncV2
	}
	if err = a.requireParticipationCaps(context.Background(), key, required); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	return true, true, nil
}

// markVisibleSentRead records an explicit read of locally signed device messages.
// The same original may be incoming on another current own-human device.
func (a *Agent) markVisibleSentRead(ids []string) error {
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	me, ok, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil {
		return err
	}
	fp := a.Self().Fingerprint()
	if !ok || !me.has(a.Address, fp) || !me.roster.Human(fp) {
		return nil
	}
	for _, id := range ids {
		var raw, recipient string
		err = tx.QueryRow(`SELECT envelope,recipient FROM outbox o WHERE id=? AND conv IS NULL AND ref_id IS NULL AND coalesce(sub,'')='' AND NOT EXISTS(SELECT 1 FROM inbox i WHERE i.id=o.id) AND NOT (`+privateReceiverSetupOutbox+`) AND NOT EXISTS(SELECT 1 FROM device_history_rows h WHERE h.storage='out' AND h.id=o.id AND (h.author_fp!=? OR h.direction!='out' OR h.recipient!=o.recipient OR h.recipient_fp!=coalesce(o.recipient_fp,'')))`, id, fp).Scan(&raw, &recipient)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		var env envelope.Envelope
		if json.Unmarshal([]byte(raw), &env) != nil || env.ID != id || env.From != a.Address || env.To != recipient || env.V == envelope.Version3 || env.VerifySig(a.Self().SignKey) != nil {
			continue
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO read_marks(owner,conv,fingerprint,lid) VALUES(?,'',?,?)`, me.info.Person, fp, id); err != nil {
			return err
		}
	}
	return a.store.done(tx.Commit())
}

// Only explicit attention gets a latest register. Legacy read triggers and
// timestamps remain append-only and cannot erase a deliberate unread mark.
const attentionMarkSchema = `
CREATE TABLE attention_marks(owner TEXT NOT NULL,conv TEXT NOT NULL,fingerprint TEXT NOT NULL,lid TEXT NOT NULL,state TEXT NOT NULL CHECK(state IN ('read','unread')),revision INTEGER NOT NULL,writer TEXT NOT NULL,PRIMARY KEY(owner,conv,fingerprint,lid));
CREATE TABLE attention_mark_copies(owner TEXT NOT NULL,recipient_fp TEXT NOT NULL,conv TEXT NOT NULL,fingerprint TEXT NOT NULL,lid TEXT NOT NULL,revision INTEGER NOT NULL,writer TEXT NOT NULL,carrier TEXT NOT NULL,PRIMARY KEY(owner,recipient_fp,conv,fingerprint,lid));
`

var errAttentionConflict = errors.New("read sync: conflicting exact attention generation")
var errAttentionScopeChanged = errors.New("attention: this chat changed; try again")

func loadAttentionMarks(q dbq, owner string, refs []protocol.ReadRef) (map[protocol.ReadRef]protocol.AttentionMark, error) {
	out := map[protocol.ReadRef]protocol.AttentionMark{}
	if owner == "" {
		return out, nil
	}
	for start := 0; start < len(refs); start += 128 {
		raw, _ := json.Marshal(refs[start:min(start+128, len(refs))])
		rows, err := q.Query(`SELECT m.conv,m.fingerprint,m.lid,m.state,m.revision,m.writer FROM json_each(?) w JOIN attention_marks m ON m.owner=? AND m.conv=json_extract(w.value,'$.conv') AND m.fingerprint=json_extract(w.value,'$.fingerprint') AND m.lid=json_extract(w.value,'$.lid')`, string(raw), owner)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var m protocol.AttentionMark
			if err = rows.Scan(&m.Conv, &m.Fingerprint, &m.LID, &m.State, &m.Rev, &m.Writer); err != nil {
				rows.Close()
				return nil, err
			}
			out[m.ReadRef] = m
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func saveAttentionMark(tx *sql.Tx, owner string, m protocol.AttentionMark) error {
	return saveAttentionMarks(tx, owner, []protocol.AttentionMark{m})
}

func saveAttentionMarks(tx *sql.Tx, owner string, marks []protocol.AttentionMark) error {
	refs := make([]protocol.ReadRef, 0, len(marks))
	for _, m := range marks {
		refs = append(refs, m.ReadRef)
	}
	current, err := loadAttentionMarks(tx, owner, refs)
	if err != nil {
		return err
	}
	for _, m := range marks {
		old := current[m.ReadRef]
		if old.Rev > 0 {
			if m.Rev < old.Rev || m.Rev == old.Rev && m.Writer < old.Writer {
				continue
			}
			if m.AttentionGeneration == old.AttentionGeneration {
				if old.State != m.State {
					return errAttentionConflict
				}
				continue
			}
		}
		if _, err = tx.Exec(`INSERT INTO attention_marks(owner,conv,fingerprint,lid,state,revision,writer) VALUES(?,?,?,?,?,?,?) ON CONFLICT(owner,conv,fingerprint,lid) DO UPDATE SET state=excluded.state,revision=excluded.revision,writer=excluded.writer`, owner, m.Conv, m.Fingerprint, m.LID, m.State, m.Rev, m.Writer); err != nil {
			return err
		}
		current[m.ReadRef] = m
	}
	return nil
}

func attentionUnread(mark protocol.AttentionMark, legacy bool) bool {
	if mark.Rev > 0 {
		return mark.State == "unread"
	}
	return legacy
}
func attentionGeneration(mark protocol.AttentionMark) *protocol.AttentionGeneration {
	if mark.State != "unread" {
		return nil
	}
	g := mark.AttentionGeneration
	return &g
}
func convAttentionRef(conv string, m ConvMessage) protocol.ReadRef {
	key := m.Key
	if key == "" {
		key = m.Claimed
	}
	return protocol.ReadRef{Conv: conv, Fingerprint: key, LID: m.LID}
}
func normalAttentionMessage(kind string) bool {
	return kind == envelope.KindMessage || kind == envelope.KindQuestion || kind == envelope.KindTask || kind == envelope.KindAnswer || kind == envelope.KindResult
}
func convAttentionEligible(m ConvMessage) bool {
	return !m.Deleted && m.Sub == "" && m.TopicEvent == nil && normalAttentionMessage(m.Kind)
}
func directAttentionEligible(m ConversationMessage) bool {
	return !m.Deleted && m.attentionSub == "" && normalAttentionMessage(m.Kind) && !(m.Kind == envelope.KindMessage && m.Status == envelope.StatusReviewNotice && m.ReplyTo == "" && len(m.Attachments) == 0)
}
func attentionOwner(q dbq) (string, error) {
	var owner string
	err := q.QueryRow(`SELECT person FROM persons WHERE state=?`, personSelf).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return owner, err
}
func (a *Agent) projectConvAttention(conv string, msgs []ConvMessage) error {
	owner, err := attentionOwner(a.store.db)
	if err != nil {
		return err
	}
	refs := make([]protocol.ReadRef, 0, len(msgs))
	for _, m := range msgs {
		if convAttentionEligible(m) {
			refs = append(refs, convAttentionRef(conv, m))
		}
	}
	marks, err := loadAttentionMarks(a.store.db, owner, refs)
	if err != nil {
		return err
	}
	for i := range msgs {
		m := &msgs[i]
		m.Attention = nil
		if !convAttentionEligible(*m) {
			m.Unread = false
			continue
		}
		mark := marks[convAttentionRef(conv, *m)]
		m.Unread = attentionUnread(mark, m.Dir == "in" && m.Unread)
		m.Attention = attentionGeneration(mark)
	}
	return nil
}
func (a *Agent) projectDirectAttention(msgs []ConversationMessage) error {
	owner, err := attentionOwner(a.store.db)
	if err != nil {
		return err
	}
	refs := make([]protocol.ReadRef, 0, len(msgs))
	for _, m := range msgs {
		if directAttentionEligible(m) {
			refs = append(refs, protocol.ReadRef{Fingerprint: m.FromKey, LID: m.ID})
		}
	}
	marks, err := loadAttentionMarks(a.store.db, owner, refs)
	if err != nil {
		return err
	}
	for i := range msgs {
		m := &msgs[i]
		m.Attention = nil
		if !directAttentionEligible(*m) {
			m.Unread = false
			continue
		}
		mark := marks[protocol.ReadRef{Fingerprint: m.FromKey, LID: m.ID}]
		m.Unread = attentionUnread(mark, m.Dir == "in" && m.Read != nil && !*m.Read)
		m.Attention = attentionGeneration(mark)
		if m.Read != nil {
			read := !m.Unread
			m.Read = &read
		}
	}
	return nil
}

// attentionScope resolves only the same complete admitted snapshot used by
// normal views. It never fetches unknown history or trusts caller author keys.
func (a *Agent) attentionScope(anchor string) (map[string]protocol.ReadRef, error) {
	var conv string
	err := a.store.db.QueryRow(`SELECT coalesce(conv,'') FROM inbox WHERE id=? UNION ALL SELECT coalesce(conv,'') FROM outbox WHERE id=? LIMIT 1`, anchor, anchor).Scan(&conv)
	if err != nil {
		return nil, err
	}
	out := map[string]protocol.ReadRef{}
	if conv != "" {
		msgs, e := a.conversationMessages(conv, "")
		if e != nil {
			return nil, e
		}
		assigned := ChatTopicAssignments(msgs)
		topic := ""
		found := false
		for _, m := range msgs {
			if m.ID == anchor && convAttentionEligible(m) {
				topic = assigned[m.LID]
				found = true
				break
			}
			for _, c := range m.Copies {
				if c.ID == anchor && convAttentionEligible(m) {
					topic = assigned[m.LID]
					found = true
					break
				}
			}
		}
		if !found {
			return nil, ErrNoMessage
		}
		for _, m := range msgs {
			if !convAttentionEligible(m) || assigned[m.LID] != topic {
				continue
			}
			ref := convAttentionRef(conv, m)
			out[m.ID] = ref
			for _, c := range m.Copies {
				out[c.ID] = ref
			}
		}
	} else {
		view, e := a.conversation(anchor, 0, 0, false)
		if e != nil {
			return nil, e
		}
		for _, m := range view.Messages {
			if directAttentionEligible(m) {
				out[m.ID] = protocol.ReadRef{Fingerprint: m.FromKey, LID: m.ID}
			}
		}
	}
	if _, ok := out[anchor]; !ok {
		return nil, ErrNoMessage
	}
	return out, nil
}

// Existing canonical views read through the store's single connection. Validate
// that snapshot after acquiring the write transaction, so an erasure, topic
// move or new control cannot slip between scope validation and the action.
// total_changes covers this connection; data_version covers other connections.
func (a *Agent) attentionActionScope(resolve func() (map[string]protocol.ReadRef, error)) (*sql.Tx, map[string]protocol.ReadRef, error) {
	const stampSQL = `SELECT total_changes(),data_version FROM pragma_data_version`
	for attempt := 0; attempt < 3; attempt++ {
		var changes, version, afterChanges, afterVersion int64
		if err := a.store.db.QueryRow(stampSQL).Scan(&changes, &version); err != nil {
			return nil, nil, err
		}
		scope, err := resolve()
		if err != nil {
			return nil, nil, err
		}
		tx, err := a.store.db.Begin()
		if err != nil {
			return nil, nil, err
		}
		if err = tx.QueryRow(stampSQL).Scan(&afterChanges, &afterVersion); err != nil {
			tx.Rollback()
			return nil, nil, err
		}
		if changes == afterChanges && version == afterVersion {
			return tx, scope, nil
		}
		tx.Rollback()
	}
	return nil, nil, errAttentionScopeChanged
}

// MarkUnread applies exactly this admitted topic snapshot, atomically. Its
// caller expands “from here”; later arrivals are never silently included.
func (a *Agent) MarkUnread(anchor string, ids []string) error {
	tx, scope, err := a.attentionActionScope(func() (map[string]protocol.ReadRef, error) { return a.attentionScope(anchor) })
	if err != nil {
		return err
	}
	defer tx.Rollback()
	refs := []protocol.ReadRef{}
	seen := map[protocol.ReadRef]bool{}
	hasAnchor := false
	for _, id := range ids {
		if id == anchor {
			hasAnchor = true
		}
		ref, ok := scope[id]
		if !ok || !protocol.ValidFingerprint(ref.Fingerprint) {
			return errors.New("unread: message is unavailable or outside this topic")
		}
		if !seen[ref] {
			refs = append(refs, ref)
			seen[ref] = true
		}
	}
	if !hasAnchor {
		return errors.New("unread: include the selected message")
	}
	owner, err := attentionOwner(tx)
	if err != nil {
		return err
	}
	allowed, err := a.nativeOwnerHumanIn(tx, a.Address, a.Self().Fingerprint())
	if err != nil {
		return err
	}
	if owner == "" || !allowed {
		return errors.New("unread: current own-human identity required")
	}
	marks, err := loadAttentionMarks(tx, owner, refs)
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO attention_marks(owner,conv,fingerprint,lid,state,revision,writer) VALUES(?,?,?,?,'unread',?,?) ON CONFLICT(owner,conv,fingerprint,lid) DO UPDATE SET state='unread',revision=excluded.revision,writer=excluded.writer`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, ref := range refs {
		old := marks[ref]
		if old.Rev == protocol.MaxAttentionRevision {
			return errors.New("unread: revision limit reached")
		}
		if _, err = stmt.Exec(owner, ref.Conv, ref.Fingerprint, ref.LID, old.Rev+1, a.Self().Fingerprint()); err != nil {
			return err
		}
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.topicTitlesChanged()
	}
	return err
}

// MarkReadWithAttention keeps ordinary read behavior. Only a complete matching
// observed generation can clear deliberate unread; omitted/stale tokens cannot.
func (a *Agent) MarkReadWithAttention(ids []string, attention map[string]protocol.AttentionGeneration) error {
	if len(attention) > 0 {
		selected := map[string]bool{}
		for _, id := range ids {
			selected[id] = true
		}
		for id, g := range attention {
			if !selected[id] || !g.Valid() {
				return errors.New("read: invalid attention generation or message")
			}
		}
		tx, resolved, err := a.attentionActionScope(func() (map[string]protocol.ReadRef, error) {
			resolved := map[string]protocol.ReadRef{}
			// Resolve each involved scope once; multiple IDs in that scope reuse it.
			for id := range attention {
				if _, ok := resolved[id]; ok {
					continue
				}
				scope, err := a.attentionScope(id)
				if err != nil {
					return nil, err
				}
				for k, v := range scope {
					resolved[k] = v
				}
			}
			return resolved, nil
		})
		if err != nil {
			return a.markReadAfterAttention(ids, err)
		}
		defer tx.Rollback()
		owner, err := attentionOwner(tx)
		if err != nil {
			return err
		}
		allowed, err := a.nativeOwnerHumanIn(tx, a.Address, a.Self().Fingerprint())
		if err != nil {
			return err
		}
		changed := false
		if allowed && owner != "" {
			refs := make([]protocol.ReadRef, 0, len(attention))
			for id := range attention {
				refs = append(refs, resolved[id])
			}
			marks, e := loadAttentionMarks(tx, owner, refs)
			if e != nil {
				return e
			}
			for id, g := range attention {
				ref, ok := resolved[id]
				if !ok {
					return ErrNoMessage
				}
				old := marks[ref]
				if old.State != "unread" || old.AttentionGeneration != g {
					continue
				}
				if g.Rev == protocol.MaxAttentionRevision {
					return errors.New("read: revision limit reached")
				}
				result, e := tx.Exec(`UPDATE attention_marks SET state='read',revision=revision+1,writer=? WHERE owner=? AND conv=? AND fingerprint=? AND lid=? AND state='unread' AND revision=? AND writer=?`, a.Self().Fingerprint(), owner, ref.Conv, ref.Fingerprint, ref.LID, g.Rev, g.Writer)
				if e != nil {
					return e
				}
				n, e := result.RowsAffected()
				if e != nil {
					return e
				}
				changed = changed || n > 0
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if changed {
			a.store.changed()
			a.topicTitlesChanged()
		}

	}
	return a.markReadAfterAttention(ids, nil)
}

// Snapshot contention defers only the generation CAS. Legacy read state still
// advances; every other attention failure keeps its old refusal.
func (a *Agent) markReadAfterAttention(ids []string, attentionErr error) error {
	if attentionErr != nil && !errors.Is(attentionErr, errAttentionScopeChanged) {
		return attentionErr
	}
	if err := a.store.markRead(ids); err != nil {
		return err
	}
	if err := a.markVisibleSentRead(ids); err != nil {
		return err
	}
	notifyDaemon(a.home)
	return nil
}
