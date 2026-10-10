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

type deviceHistoryRef struct {
	seq         int64
	storage, id string
}

// Same existing history wake, bounded journal pages, no timer or new worker.
func (a *Agent) syncDeviceHistory() (bool, error) {
	own, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok {
		return false, err
	}
	more := false
	checked := map[string]bool{}
	for _, dev := range own.roster.Devices {
		if dev.Address == a.Address {
			continue
		}
		if err = historyRecoveryCurrent(a.store.db, a.Self(), dev); err != nil {
			continue
		}
		// Nothing is sealed for a device that cannot read it now: its job
		// keeps its cursor until it can (ownSyncReader).
		if due, e := a.store.deviceHistoryDue(dev); e != nil {
			return more, e
		} else if due && !a.ownSyncReader(context.Background(), dev, protocol.CapDeviceHistory, checked) {
			continue
		}
		m, e := a.deviceHistoryPage(dev)
		if e != nil {
			return more, e
		}
		more = more || m
	}
	return more, nil
}

func (a *Agent) deviceHistoryPage(dev identity.Public) (bool, error) {
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
	if err = historyRecoveryCurrent(tx, a.Self(), dev); err != nil {
		return false, err
	}
	if full, e := syncWindowFull(tx, dev); e != nil || full {
		return false, e
	}
	own, ok, err := scanPersonIn(tx, "state = ?", personSelf)
	if err != nil || !ok {
		return false, err
	}
	var ceiling, older, tail int64
	if err = tx.QueryRow(`SELECT coalesce(max(seq),0) FROM device_history_sources`).Scan(&ceiling); err != nil {
		return false, err
	}
	err = tx.QueryRow(`SELECT older,tail FROM device_history_jobs WHERE device=? AND fingerprint=?`, dev.Address, dev.Fingerprint()).Scan(&older, &tail)
	if errors.Is(err, sql.ErrNoRows) {
		older, tail = ceiling+1, ceiling
		if _, err = tx.Exec(`INSERT INTO device_history_jobs VALUES(?,?,?,?)`, dev.Address, dev.Fingerprint(), older, tail); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	}
	read := func(filter string, args ...any) ([]deviceHistoryRef, error) {
		rows, e := tx.Query(`SELECT seq,storage,id FROM device_history_sources WHERE `+filter, args...)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		var refs []deviceHistoryRef
		for rows.Next() {
			var r deviceHistoryRef
			if e = rows.Scan(&r.seq, &r.storage, &r.id); e != nil {
				return nil, e
			}
			refs = append(refs, r)
		}
		return refs, rows.Err()
	}
	newer, err := read("seq>? AND seq<=? ORDER BY seq LIMIT ?", tail, ceiling, historyPage)
	if err != nil {
		return false, err
	}
	old, err := read("seq<? ORDER BY seq DESC LIMIT ?", older, historyPage)
	if err != nil {
		return false, err
	}
	if len(newer) > 0 {
		tail = newer[len(newer)-1].seq
	} else {
		tail = ceiling
	}
	if len(old) > 0 {
		older = old[len(old)-1].seq
	}
	if len(old) < historyPage {
		older = 0
	}
	refs := append(newer, old...)
	// Reuse the existing external-evidence sweep. One bounded page per turn;
	// unchanged pending rows do not wake another completed sweep.
	a.convWork.mu.Lock()
	if a.convWork.historyDeferred == nil {
		a.convWork.historyDeferred = map[string]historyDeferredScan{}
	}
	sweep := a.convWork.historyDeferred["direct/"+dev.Fingerprint()]
	a.convWork.mu.Unlock()
	if len(newer) > 0 {
		sweep = historyDeferredScan{}
	}
	if !sweep.done {
		rows, e := tx.Query(`SELECT storage,id FROM device_history_pending WHERE recipient_fp=? AND (storage,id)>(?,?) ORDER BY storage,id LIMIT ?`, dev.Fingerprint(), sweep.dir, sweep.id, historyPage)
		if e != nil {
			return false, e
		}
		n := 0
		for rows.Next() {
			var r deviceHistoryRef
			if e = rows.Scan(&r.storage, &r.id); e != nil {
				rows.Close()
				return false, e
			}
			refs = append(refs, r)
			sweep.dir, sweep.id = r.storage, r.id
			n++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		sweep.done = n < historyPage
	}
	done, visiting := map[string]bool{}, map[string]bool{}
	var copies []outCopy
	var queue func(deviceHistoryRef) error
	queue = func(ref deviceHistoryRef) error {
		key := ref.storage + "/" + ref.id
		if done[key] {
			return nil
		}
		if visiting[key] || len(visiting) >= 64 || len(copies) >= 4*historyPage {
			return ErrGroupContextPending
		}
		visiting[key] = true
		defer delete(visiting, key)
		r, e := a.deviceHistorySource(tx, ref.storage, ref.id)
		if errors.Is(e, sql.ErrNoRows) {
			done[key] = true
			return nil
		}
		if e != nil {
			return e
		}
		var erased int
		if e = tx.QueryRow(`SELECT count(*) FROM conv_erased WHERE conv='' AND key=? AND lid=?`, r.item.FromKey, r.item.ID).Scan(&erased); e != nil {
			return e
		}
		if erased != 0 {
			done[key] = true
			return nil
		}
		parent := r.item.ReplyTo
		if r.item.Ref != nil {
			parent = r.item.Ref.ID
		}
		if parent != "" && parent != r.id {
			p, pe := a.deviceHistoryOriginal(tx, parent)
			if pe != nil && !errors.Is(pe, sql.ErrNoRows) {
				return pe
			}
			if pe == nil {
				if e = queue(deviceHistoryRef{storage: p.storage, id: parent}); e != nil {
					return e
				}
			}
		}
		if e = a.deviceHistoryProjection(tx, &r, own.info.Person); e != nil {
			return e
		}
		if e = a.deviceHistoryBinding(tx, r); e != nil {
			return e
		}
		if e = storeDeviceHistoryMetadata(tx, r); e != nil {
			return e
		}
		hash := deviceHistoryHash(r)
		var prevHash, carrier, state string
		e = tx.QueryRow(`SELECT c.hash,c.carrier,coalesce(o.state,'') FROM device_history_copies c LEFT JOIN outbox o ON o.id=c.carrier WHERE c.recipient_fp=? AND c.author=? AND c.id=?`, dev.Fingerprint(), r.item.FromKey, r.id).Scan(&prevHash, &carrier, &state)
		if e == nil {
			if prevHash != hash {
				return errHistoryCatchupConflict
			}
			if state != "" && state != stateNotDelivered && state != protocol.StateExpired {
				done[key] = true
				return nil
			}
		} else if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		// The original sender already has its original outgoing row. A copy
		// arriving on its recipient still matters when that device was offline.
		if r.item.From == dev.Address && r.item.FromKey == dev.Fingerprint() {
			done[key] = true
			return nil
		}
		// Nor does the device that forwarded it here: it holds it already.
		if via, e := historyVia(tx, historySourceRow{dir: ref.storage, in: envelope.Inner{ID: r.id}}); e != nil {
			return e
		} else if via == dev.Address {
			done[key] = true
			return nil
		}
		// Its exact direct recipient gets the original itself. Copy only an
		// original that device missed; one still in transit waits pending.
		if r.to == dev.Address && r.toKey == dev.Fingerprint() {
			if e = deviceHistoryRecipientMissed(tx, ref.storage, r); e != nil {
				if errors.Is(e, errDeviceHistoryRecipientHas) {
					done[key] = true
					return nil
				}
				return e
			}
		}
		raw, e := json.Marshal(r.item)
		if e != nil {
			return e
		}
		body, e := json.Marshal(protocol.DeviceHistory{V: 1, Person: own.info.Person, Roster: own.info.Roster, Recipient: r.to, RecipientKey: r.toKey, Item: raw})
		if e != nil {
			return e
		}
		if _, e = protocol.ParseDeviceHistory(body); e != nil {
			return errors.Join(errDeviceHistoryBlocked, e)
		}
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: dev.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubDeviceHistory, Replica: true, Body: string(body)}
		recipient, e := dev.Recipient()
		if e != nil {
			return e
		}
		env, e := envelope.Seal(in, a.id.Sign, recipient)
		if e != nil {
			return e
		}
		copies = append(copies, outCopy{env: env, in: in, state: stateQueued, required: protocol.CapDeviceHistory, recipientFP: dev.Fingerprint()})
		if _, e = tx.Exec(`INSERT INTO device_history_copies VALUES(?,?,?,?,?) ON CONFLICT(recipient_fp,author,id) DO UPDATE SET carrier=excluded.carrier`, dev.Fingerprint(), r.item.FromKey, r.id, hash, env.ID); e != nil {
			return e
		}
		if _, e = tx.Exec(`DELETE FROM device_history_pending WHERE recipient_fp=? AND storage=? AND id=?`, dev.Fingerprint(), ref.storage, ref.id); e != nil {
			return e
		}
		done[key] = true
		return nil
	}
	for _, ref := range refs {
		if e := queue(ref); e != nil {
			if !errors.Is(e, errDeviceHistoryBlocked) && !errors.Is(e, ErrGroupContextPending) {
				return false, e
			}
			if _, err = tx.Exec(`INSERT OR IGNORE INTO device_history_pending VALUES(?,?,?)`, dev.Fingerprint(), ref.storage, ref.id); err != nil {
				return false, err
			}
		} else if _, err = tx.Exec(`DELETE FROM device_history_pending WHERE recipient_fp=? AND storage=? AND id=?`, dev.Fingerprint(), ref.storage, ref.id); err != nil {
			return false, err
		}
	}
	if err = historyRecoveryCurrent(tx, a.Self(), dev); err != nil {
		return false, err
	}
	if err = insertCopies(tx, copies); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE device_history_jobs SET older=?,tail=? WHERE device=? AND fingerprint=?`, older, tail, dev.Address, dev.Fingerprint()); err != nil {
		return false, err
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return false, err
	}
	a.convWork.mu.Lock()
	a.convWork.historyDeferred["direct/"+dev.Fingerprint()] = sweep
	a.convWork.mu.Unlock()
	return older != 0 || tail < ceiling || !sweep.done, nil
}

var errDeviceHistoryRecipientHas = errors.New("device history: the direct recipient holds the original")

// deviceHistoryRecipientMissed decides a copy to the original's own direct
// recipient. Only this device's own send state says what that device got:
// delivered or quarantined means it stored the original; expired, not
// delivered or failed means it missed it and gets the copy. Anything else is
// still in transit and stays pending, so the outcome decides on a later sweep
// instead of queueing the same item twice behind the original. An original
// imported from another own device is that sender's to cover.
func deviceHistoryRecipientMissed(q dbq, storage string, r deviceHistoryRow) error {
	if storage != "out" {
		return errDeviceHistoryRecipientHas
	}
	var state string
	err := q.QueryRow(`SELECT state FROM outbox WHERE id=? AND recipient=? AND recipient_fp=?`, r.id, r.to, r.toKey).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // no exact send record here: keep the copy
	}
	switch {
	case err != nil:
		return err
	case state == protocol.StateDelivered || state == protocol.StateQuarantined:
		return errDeviceHistoryRecipientHas
	case state == protocol.StateExpired || state == stateNotDelivered || state == stateFailed:
		return nil
	}
	return errors.Join(errDeviceHistoryBlocked, errors.New("device history: original still in transit to its recipient"))
}

// Status/agent identity is inherited only from the exact accepted request.
func (a *Agent) deviceHistoryBinding(q dbq, r deviceHistoryRow) error {
	h := r.item
	ref := h.ReplyTo
	if h.Ref != nil {
		ref = h.Ref.ID
	}
	if ref == "" {
		return nil
	}
	parent, err := a.deviceHistoryOriginal(q, ref)
	if errors.Is(err, sql.ErrNoRows) {
		if h.Ref != nil || h.AgentID != "" {
			return ErrGroupContextPending
		}
		return nil
	}
	if err != nil {
		return err
	}
	if h.Sub == "" && (h.Kind == envelope.KindAnswer || h.Kind == envelope.KindResult || h.Status == envelope.StatusProgress) {
		if h.From != parent.to || r.to != parent.item.From || parent.toKey != "" && h.FromKey != parent.toKey {
			return errors.Join(errDeviceHistoryBlocked, errors.New("device history: reply differs from original endpoints"))
		}
	}
	if h.Ref != nil {
		if h.Ref.Fingerprint != parent.item.FromKey {
			return errors.Join(errDeviceHistoryBlocked, errors.New("device history: control names another original key"))
		}
		switch h.Sub {
		case envelope.SubStatus:
			if parent.item.Kind != envelope.KindQuestion && parent.item.Kind != envelope.KindTask || h.From != parent.to || parent.toKey == "" || h.FromKey != parent.toKey {
				return errors.Join(errDeviceHistoryBlocked, errors.New("device history: status host differs from original request"))
			}
		case envelope.SubRevision, envelope.SubRetraction:
			if h.FromKey != parent.item.FromKey {
				return errors.Join(errDeviceHistoryBlocked, errors.New("device history: changed original author"))
			}
		case envelope.SubReaction:
			if h.FromKey != parent.item.FromKey && (parent.toKey == "" || h.FromKey != parent.toKey) {
				return errors.Join(errDeviceHistoryBlocked, errors.New("device history: reaction from another key"))
			}
		}
	}
	if h.AgentID != "" && h.Sub == "" {
		t := parent.item.Target
		if t == nil || t.Address != h.From || t.Fingerprint != h.FromKey || t.AgentID != h.AgentID {
			return errors.Join(errDeviceHistoryBlocked, errors.New("device history: named output differs from exact requested agent"))
		}
	}
	return nil
}

func (a *Agent) mayDeliverDeviceHistory(env envelope.Envelope) (bool, bool, error) {
	var sub, body, fp, state string
	err := a.store.db.QueryRow(`SELECT coalesce(sub,''),body,coalesce(recipient_fp,''),state FROM outbox WHERE id=?`, env.ID).Scan(&sub, &body, &fp, &state)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sub != envelope.SubDeviceHistory && sub != envelope.SubDeviceFile {
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
	w, parseErr := protocol.ParseDeviceHistory([]byte(body))
	if sub == envelope.SubDeviceFile {
		f, e := protocol.ParseDeviceFile([]byte(body))
		w.Person, w.Roster, parseErr = f.Person, f.Roster, e
	}
	check := func() (identity.Public, bool, error) {
		key, pending, found, e := a.store.peer(env.To)
		if e != nil {
			return key, false, e
		}
		ok := parseErr == nil && found && pending == nil && key.Fingerprint() == fp && readSyncAuthority(a.store.db, protocol.ReadSync{Person: w.Person, Roster: w.Roster}, env.From, a.Self().Fingerprint(), env.To, fp) == nil
		if ok && sub == envelope.SubDeviceFile {
			f, _ := protocol.ParseDeviceFile([]byte(body))
			var m fileMsg
			if decodeStrict(f.Item, &m) != nil {
				ok = false
			} else {
				_, e := a.deviceFileSource(a.store.db, m)
				ok = e == nil
			}
		}
		if !ok {
			e = a.store.setOutboxState(env.ID, stateNotDelivered, "direct history owner or device authority changed", "")
		}
		return key, ok, e
	}
	key, ok, err := check()
	if err != nil || !ok {
		return true, false, err
	}
	if err = a.requireParticipationCaps(context.Background(), key, protocol.CapDeviceHistory); err != nil {
		if errors.Is(err, errAgentIdentityUnsupported) {
			return true, false, a.store.setOutboxState(env.ID, stateConvWaiting, WaitPeerUpdate+err.Error(), "")
		}
		return true, false, err
	}
	_, ok, err = check()
	return true, ok, err
}
