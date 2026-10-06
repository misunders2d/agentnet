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
		key, pending, found, e := a.store.peer(dev.Address)
		if e != nil {
			return false, e
		}
		if pending != nil || found && key.Fingerprint() != dev.Fingerprint() {
			continue
		}
		// Batch up to 64 exact references; indexed copy mappings point to existing outbox rows
		// are the reconciliation marker. Terminal failures may recover on a wake.
		rows, e := a.store.db.Query(`SELECT r.conv,r.fingerprint,r.lid FROM read_marks r WHERE r.owner=? AND NOT EXISTS(SELECT 1 FROM read_mark_copies c JOIN outbox o ON o.id=c.carrier WHERE c.owner=r.owner AND c.recipient_fp=? AND c.conv=r.conv AND c.fingerprint=r.fingerprint AND c.lid=r.lid AND o.state IN ('queued','waiting','custody','delivered')) ORDER BY r.conv,r.fingerprint,r.lid LIMIT ?`, me.info.Person, dev.Fingerprint(), protocol.MaxReadRefs)
		if e != nil {
			return false, e
		}
		var refs []protocol.ReadRef
		for rows.Next() {
			var ref protocol.ReadRef
			if e = rows.Scan(&ref.Conv, &ref.Fingerprint, &ref.LID); e != nil {
				break
			}
			refs = append(refs, ref)
		}
		rowErr := rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		if rowErr != nil {
			return false, rowErr
		}
		if len(refs) == 0 {
			continue
		}
		r := protocol.ReadSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Refs: refs}
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
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapReadSync, recipientFP: dev.Fingerprint()})
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
		if len(r.Refs) == protocol.MaxReadRefs {
			more = true
		}
		for _, ref := range r.Refs {
			if _, err = tx.Exec(`INSERT OR REPLACE INTO read_mark_copies(owner,recipient_fp,conv,fingerprint,lid,carrier) VALUES(?,?,?,?,?,?)`, r.Person, c.recipientFP, ref.Conv, ref.Fingerprint, ref.LID, c.env.ID); err != nil {
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
	if held {
		if _, err = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
			return err
		}
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
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapReadSync); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	return true, true, nil
}
