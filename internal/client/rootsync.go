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

// Empty DMs have no HistoryItem. A root-sync replica carries their unchanged
// signed root, not a fabricated turn. Reconciliation uses existing durable
// outbox rows on connection/local-change wakes; no cursor reset, timer or new
// service. crs1 is explicit, so an older person2/rm1 reader waits for an update.
func rootSyncAuthority(q dbq, root protocol.ConvRoot, from, fromFP, to, toFP string) error {
	if root.Kind != protocol.ConvKindDM || root.Validate() != nil || from == to {
		return errors.New("root sync: invalid DM or devices")
	}
	me, ok, err := scanPersonIn(q, "state = ?", personSelf)
	if err != nil {
		return err
	}
	if !ok || !me.has(from, fromFP) || !me.has(to, toFP) || !me.roster.Human(fromFP) || !me.roster.Human(toFP) {
		return errors.New("root sync: only current own-human devices may share chats")
	}
	if _, ok := root.Member(me.info.Person); !ok {
		return errors.New("root sync: own person is not a member")
	}
	for _, m := range root.Members {
		p, ok, err := personByIDIn(q, m.Person)
		if err != nil {
			return err
		}
		if !ok {
			return ErrNoPerson
		}
		if p.info.State == personConflict {
			return errPersonConflict
		}
		bound, err := inChainIn(q, m.Person, m.Roster)
		if err != nil {
			return err
		}
		if !bound {
			return errRootInvalid
		}
	}
	var raw string
	if err := q.QueryRow(`SELECT record FROM person_chain WHERE person=? AND hash=?`, root.Creator.Person, root.Creator.Roster).Scan(&raw); err != nil {
		return err
	}
	r, err := protocol.ParsePersonRoster([]byte(raw))
	if err != nil {
		return err
	}
	for _, d := range r.Devices {
		if d.Address == root.Creator.Address && d.Fingerprint() == root.Creator.Fingerprint {
			return root.Verify(d.SignKey)
		}
	}
	return errRootInvalid
}

func (a *Agent) rootSyncCopy(root protocol.ConvRoot, raw []byte, dev identity.Public) (outCopy, error) {
	if err := rootSyncAuthority(a.store.db, root, a.Address, a.Self().Fingerprint(), dev.Address, dev.Fingerprint()); err != nil {
		return outCopy{}, err
	}
	recipient, err := dev.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage,
		Conv: root.ID(), LID: protocol.NewID(), Root: raw, Sub: envelope.SubRootSync, Replica: true, Body: `{"v":1}`}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	return outCopy{env: env, in: in, state: stateQueued, required: protocol.CapRootSync, recipientFP: dev.Fingerprint()}, err
}

func (a *Agent) rootSyncPresent(q dbq, conv string, dev identity.Public) (bool, error) {
	rows, err := q.Query(`SELECT envelope FROM outbox WHERE conv=? AND recipient=? AND recipient_fp=? AND sub=? AND required_cap=? AND state IN ('queued','waiting','custody','delivered','quarantined')`, conv, dev.Address, dev.Fingerprint(), envelope.SubRootSync, protocol.CapRootSync)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var env envelope.Envelope
		if json.Unmarshal([]byte(raw), &env) == nil && env.V == envelope.Version2 && env.From == a.Address && env.To == dev.Address && env.Kind == envelope.KindMessage && !env.Attn && len(env.Blobs) == 0 && env.VerifySig(a.Self().SignKey) == nil {
			return true, nil
		}
	}
	return false, rows.Err()
}

// syncRoots queues at most one page of missing root copies. The outbox is the
// durable reconciliation marker, including waiting and quarantined copies. Completed
// message snapshot positions never change. A terminal failed carrier may recover
// on a later existing wake after its authority becomes valid again.
func (a *Agent) syncRoots() (bool, error) {
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
	rows, err := a.store.db.Query(`SELECT root FROM conversations WHERE kind=? ORDER BY id`, protocol.ConvKindDM)
	if err != nil {
		return false, err
	}
	var roots []protocol.ConvRoot
	var raws [][]byte
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		root, e := protocol.ParseConvRoot([]byte(raw))
		if e != nil {
			err = e
			break
		}
		roots = append(roots, root)
		raws = append(raws, []byte(raw))
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if rowErr != nil {
		return false, rowErr
	}
	var copies []outCopy
	for i, root := range roots {
		if _, member := root.Member(me.info.Person); !member || a.store.convDeleted(root.ID(), a.Self().Fingerprint()) {
			continue
		}
		for _, dev := range me.roster.Devices {
			if dev.Address == a.Address || !me.roster.Human(dev.Fingerprint()) {
				continue
			}
			if err := rootSyncAuthority(a.store.db, root, a.Address, a.Self().Fingerprint(), dev.Address, dev.Fingerprint()); err != nil {
				continue
			}
			present, err := a.rootSyncPresent(a.store.db, root.ID(), dev)
			if err != nil {
				return false, err
			}
			if present {
				continue
			}
			key, pending, found, err := a.store.peer(dev.Address)
			if err != nil {
				return false, err
			}
			// A verified link does not pin peers; ordinary delivery performs the
			// directory check. Seal to the signed roster key, but never replace an
			// existing pin or ignore a pending key change.
			if pending != nil || (found && key.Fingerprint() != dev.Fingerprint()) {
				continue
			}
			copy, err := a.rootSyncCopy(root, raws[i], dev)
			if err != nil {
				return false, err
			}
			copies = append(copies, copy)
			if len(copies) == historyPage {
				break
			}
		}
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
	var fresh []outCopy
	for _, c := range copies {
		root, err := protocol.ParseConvRoot(c.in.Root)
		if err != nil {
			return false, err
		}
		if err = rootSyncAuthority(tx, root, a.Address, a.Self().Fingerprint(), c.in.To, c.recipientFP); err != nil {
			return false, err
		}
		dev, _ := me.device(c.in.To)
		present, err := a.rootSyncPresent(tx, c.in.Conv, dev)
		if err != nil {
			return false, err
		}
		if !present {
			fresh = append(fresh, c)
		}
	}
	if err = insertCopies(tx, fresh); err != nil {
		return false, err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return false, err
	}
	return len(copies) == historyPage, nil
}

func (a *Agent) admitRootSync(ctx context.Context, env envelope.Envelope, in envelope.Inner, root protocol.ConvRoot, sender identity.Public, sp personRow, held bool, hold func(string, string) error) error {
	me, ok, err := a.store.selfPerson(a.Address)
	if err != nil {
		return err
	}
	if !ok || sp.info.Person != me.info.Person || !me.has(env.From, sender.Fingerprint()) || !me.roster.Human(sender.Fingerprint()) || !me.has(a.Address, a.Self().Fingerprint()) || !me.roster.Human(a.Self().Fingerprint()) {
		return hold(reasonInvalid, "root sync comes only between current own-human devices")
	}
	if err := a.verifyRoot(ctx, root, sp); err != nil {
		return hold(reasonProof, err.Error())
	}
	for _, m := range root.Members {
		bound, err := a.boundIn(ctx, m.Person, m.Roster)
		if err != nil {
			if retryable(err) {
				return err
			}
			return hold(reasonProof, err.Error())
		}
		if !bound {
			return hold(reasonInvalid, "root sync member is not bound to its verified roster")
		}
	}
	peer := root.Members[0].Person
	if peer == me.info.Person {
		peer = root.Members[1].Person
	}
	result, err := a.store.addConvInbox(in, sender.Fingerprint(), "", held, func(tx *sql.Tx) error {
		if err := rootSyncAuthority(tx, root, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO conversations(id,root,kind,peer,pinned_at) VALUES(?,?,?,?,?)`, root.ID(), string(in.Root), root.Kind, peer, time.Now().Unix()); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE inbox SET read_at=? WHERE id=?`, time.Now().Unix(), in.ID)
		return err
	})
	if errors.Is(err, errPersonConflict) {
		return hold(reasonConflict, err.Error())
	}
	if err != nil {
		return err
	}
	if result == admitConflict {
		return hold(reasonConflict, "root sync logical ID conflicts")
	}
	a.convWork.due(convHistory)
	a.kickNow()
	return nil
}

// rootSyncDelivery rechecks current roster/human authority and the exact sealed
// recipient key before every handover, including after restart or a key change.
func (a *Agent) rootSyncDelivery(ctx context.Context, env envelope.Envelope, fp string) (bool, error) {
	if err := a.refreshRecipientPerson(ctx, env.To, map[string]error{}); err != nil {
		return false, err
	}
	key, pending, found, err := a.store.peer(env.To)
	if err != nil {
		return false, err
	}
	if !found || pending != nil || key.Fingerprint() != fp {
		a.store.setOutboxState(env.ID, stateNotDelivered, "root sync recipient key changed", "")
		return false, nil
	}
	var conv string
	if err = a.store.db.QueryRow(`SELECT conv FROM outbox WHERE id=?`, env.ID).Scan(&conv); err != nil {
		return false, err
	}
	root, _, found, err := a.store.conversation(conv)
	if err != nil {
		return false, err
	}
	if !found {
		return false, nil
	}
	err = rootSyncAuthority(a.store.db, root, a.Address, a.Self().Fingerprint(), env.To, fp)
	if errors.Is(err, errPersonConflict) {
		return false, nil
	}
	if err != nil {
		a.store.setOutboxState(env.ID, stateNotDelivered, err.Error(), "")
		return false, nil
	}
	return true, nil
}
