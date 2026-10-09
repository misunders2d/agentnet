package client

import (
	"bytes"
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

// Views stay separate from local intents: recovery may publish accepted local
// invitations, but can never act on a sibling device's mirrored status.
const ownInvitationSchema = `
CREATE TABLE own_invitation_views(id TEXT NOT NULL, source TEXT NOT NULL, source_fp TEXT NOT NULL, revision INTEGER NOT NULL, payload BLOB NOT NULL, PRIMARY KEY(id,source_fp));
CREATE TABLE own_invitation_copies(id TEXT NOT NULL, source_fp TEXT NOT NULL, revision INTEGER NOT NULL, recipient_fp TEXT NOT NULL, carrier TEXT NOT NULL, PRIMARY KEY(id,source_fp,revision,recipient_fp));
`

func invitationAuthority(q dbq, r protocol.InvitationSync, from, fromFP, to, toFP string) error {
	return readSyncAuthority(q, protocol.ReadSync{Person: r.Person, Roster: r.Roster}, from, fromFP, to, toFP)
}

var errInvitationViewConflict = errors.New("invitation sync: conflicting revision")

func saveInvitationView(q *sql.Tx, r protocol.InvitationSync, from, fp string) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	var revision int64
	var old []byte
	err = q.QueryRow(`SELECT revision,payload FROM own_invitation_views WHERE id=? AND source_fp=?`, r.ID, fp).Scan(&revision, &old)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && revision > r.Revision {
		return nil
	}
	if err == nil && revision == r.Revision {
		if !bytes.Equal(old, raw) {
			return errInvitationViewConflict
		}
		return nil
	}
	_, err = q.Exec(`INSERT OR REPLACE INTO own_invitation_views(id,source,source_fp,revision,payload) VALUES(?,?,?,?,?)`, r.ID, from, fp, r.Revision, raw)
	return err
}

func (a *Agent) syncInvitations() (bool, error) {
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
	rows, err := tx.Query(`SELECT id FROM group_invitations WHERE direction='out' AND peer_address=? AND peer_fp=? AND peer_person=? ORDER BY rowid`, a.Address, fp, me.info.Person)
	if err != nil {
		return false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	// A persisted snapshot gives every local change a monotonic version without
	// timestamps or altering any decision/publication path.
	for _, id := range ids {
		local, e := groupInvitationIn(tx, id, "out")
		if e != nil {
			return false, e
		}
		r := protocol.InvitationSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, ID: id, Revision: 1, Status: local.State, Proposal: local.Proposal}
		var old []byte
		e = tx.QueryRow(`SELECT payload FROM own_invitation_views WHERE id=? AND source_fp=?`, id, fp).Scan(&old)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return false, e
		}
		if e == nil {
			prev, e := protocol.ParseInvitationSync(old)
			if e != nil {
				return false, e
			}
			r.Revision = prev.Revision
			raw, _ := json.Marshal(r)
			if bytes.Equal(raw, old) {
				continue
			}
			r.Revision++
		}
		raw, _ := json.Marshal(r)
		if _, e = protocol.ParseInvitationSync(raw); e != nil {
			return false, e
		}
		if e = saveInvitationView(tx, r, a.Address, fp); e != nil {
			return false, e
		}
	}
	var copies []outCopy
	for _, dev := range me.roster.Devices {
		if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
			continue
		}
		rows, e := tx.Query(`SELECT v.payload FROM own_invitation_views v WHERE v.source=? AND v.source_fp=? AND NOT EXISTS(SELECT 1 FROM own_invitation_copies c JOIN outbox o ON o.id=c.carrier WHERE c.id=v.id AND c.source_fp=v.source_fp AND c.revision=v.revision AND c.recipient_fp=? AND o.state IN ('queued','waiting','custody','delivered')) ORDER BY v.id LIMIT ?`, a.Address, fp, dev.Fingerprint(), historyPage-len(copies))
		if e != nil {
			return false, e
		}
		var pending [][]byte
		for rows.Next() {
			var raw []byte
			if e = rows.Scan(&raw); e != nil {
				break
			}
			pending = append(pending, raw)
		}
		if e == nil {
			e = rows.Err()
		}
		rows.Close()
		if e != nil {
			return false, e
		}
		for _, raw := range pending {
			r, e := protocol.ParseInvitationSync(raw)
			if e != nil {
				return false, e
			}
			if e = invitationAuthority(tx, r, a.Address, fp, dev.Address, dev.Fingerprint()); e != nil {
				continue
			}
			recipient, e := dev.Recipient()
			if e != nil {
				return false, e
			}
			in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubInvitationSync, Replica: true, Body: string(raw)}
			env, e := envelope.Seal(in, a.id.Sign, recipient)
			if e != nil {
				return false, e
			}
			copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapOwnSyncV2, recipientFP: dev.Fingerprint()})
		}
		if len(copies) == historyPage {
			break
		}
	}
	if err = insertCopies(tx, copies); err != nil {
		return false, err
	}
	for _, c := range copies {
		r, _ := protocol.ParseInvitationSync([]byte(c.in.Body))
		if _, err = tx.Exec(`INSERT OR REPLACE INTO own_invitation_copies(id,source_fp,revision,recipient_fp,carrier) VALUES(?,?,?,?,?)`, r.ID, fp, r.Revision, c.recipientFP, c.env.ID); err != nil {
			return false, err
		}
	}
	return len(copies) == historyPage, a.store.done(tx.Commit())
}

func (a *Agent) admitInvitationSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	r, err := protocol.ParseInvitationSync([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	if err = a.refreshRecipientPerson(ctx, env.From, map[string]error{}); err != nil {
		return err
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = invitationAuthority(tx, r, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	if err = saveInvitationView(tx, r, env.From, sender.Fingerprint()); err != nil {
		if errors.Is(err, errInvitationViewConflict) {
			tx.Rollback()
			return hold(reasonInvalid, err.Error())
		}
		return err
	}
	if held {
		if _, err = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
			return err
		}
	}
	return a.store.done(tx.Commit())
}

func (a *Agent) ownInvitationViews() ([]GroupInvitationInfo, error) {
	rows, err := a.store.db.Query(`SELECT source,source_fp,payload FROM own_invitation_views WHERE source_fp<>? ORDER BY rowid`, a.Self().Fingerprint())
	if err != nil {
		return nil, err
	}
	type view struct {
		from, fp string
		raw      []byte
	}
	var views []view
	for rows.Next() {
		var v view
		if err = rows.Scan(&v.from, &v.fp, &v.raw); err != nil {
			break
		}
		views = append(views, v)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []GroupInvitationInfo{}
	for _, v := range views {
		r, e := protocol.ParseInvitationSync(v.raw)
		if e != nil {
			return nil, e
		}
		if invitationAuthority(a.store.db, r, v.from, v.fp, a.Address, a.Self().Fingerprint()) != nil {
			continue
		}
		result = append(result, GroupInvitationInfo{ID: r.ID, Direction: "out", State: r.Status, Inviter: v.from, Proposal: r.Proposal})
	}
	return result, nil
}

func (a *Agent) mayDeliverInvitationSync(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubInvitationSync {
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
	r, err := protocol.ParseInvitationSync([]byte(body))
	if err == nil {
		err = invitationAuthority(a.store.db, r, env.From, a.Self().Fingerprint(), env.To, fp)
	}
	key, pending, found, e := a.store.peer(env.To)
	if e != nil {
		return true, false, e
	}
	if err != nil || !found || pending != nil || key.Fingerprint() != fp {
		return true, false, a.store.setOutboxState(env.ID, stateNotDelivered, "invitation view owner or device authority changed", "")
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapOwnSyncV2); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	// Profile I/O may race a removal or a pending-key transition.
	err = invitationAuthority(a.store.db, r, env.From, a.Self().Fingerprint(), env.To, fp)
	key, pending, found, e = a.store.peer(env.To)
	if e != nil {
		return true, false, e
	}
	if err != nil || !found || pending != nil || key.Fingerprint() != fp {
		return true, false, a.store.setOutboxState(env.ID, stateNotDelivered, "invitation view owner or device authority changed", "")
	}
	return true, true, nil
}
