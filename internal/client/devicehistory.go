package client

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type deviceHistoryRow struct {
	storage, id, to, toKey, peer, dir string
	item                              HistoryItem
}

var errDeviceHistoryBlocked = errors.New("device history source unavailable")

// An ID shared by different accepted authors is not an exact request reference.
func (a *Agent) deviceHistoryOriginal(q dbq, id string) (deviceHistoryRow, error) {
	var result deviceHistoryRow
	for _, storage := range []string{"out", "in"} {
		r, err := a.deviceHistorySource(q, storage, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return result, err
		}
		if result.id != "" && deviceHistoryHash(result) != deviceHistoryHash(r) {
			return result, errHistoryCatchupConflict
		}
		result = r
	}
	if result.id == "" {
		return result, sql.ErrNoRows
	}
	return result, nil
}

func deviceHistoryInner(r deviceHistoryRow) envelope.Inner {
	in := r.item.inner("")
	in.To, in.LID, in.Replica, in.V = r.to, "", false, envelope.Version
	if in.Sub != "" {
		in.V = envelope.Version3
	}
	return in
}

func deviceHistoryHash(r deviceHistoryRow) string {
	h := r.item
	h.At = 0 // another own device may have received the same original later
	b, _ := json.Marshal(struct {
		Item HistoryItem
		To   string
	}{h, r.to})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (a *Agent) deviceHistorySource(q dbq, storage, id string) (deviceHistoryRow, error) {
	r := deviceHistoryRow{storage: storage, id: id}
	var key, target, refID, refFP, recipient string
	var at int64
	in := envelope.Inner{ID: id, LID: id}
	var err error
	if storage == "in" {
		err = q.QueryRow(`SELECT sender,coalesce(verified_by,claimed_fp,''),ts,kind,body,coalesce(reply_to,''),coalesce(status,''),coalesce(sub,''),coalesce(origin,''),coalesce(target,''),coalesce(ref_id,''),coalesce(ref_fp,''),CASE WHEN kind IN ('question','task') THEN '' ELSE coalesce(agent_id,'') END,coalesce(received_ms,received_at*1000) FROM inbox WHERE id=? AND conv IS NULL AND local=0 AND (receiver_route IS NULL OR json_extract(receiver_route,'$.op')='request')`, id).Scan(&in.From, &key, &in.TS, &in.Kind, &in.Body, &in.ReplyTo, &in.Status, &in.Sub, &in.Origin, &target, &refID, &refFP, &in.AgentID, &at)
		r.to, r.toKey = a.Address, a.Self().Fingerprint()
	} else if storage == "out" {
		err = q.QueryRow(`SELECT recipient,coalesce(recipient_fp,''),json_extract(envelope,'$.ts'),json_extract(envelope,'$.kind'),body,coalesce(reply_to,''),coalesce(status,''),coalesce(sub,''),coalesce(origin,''),coalesce(target,''),coalesce(ref_id,''),coalesce(ref_fp,''),coalesce(agent_id,''),coalesce(created_ms,created_at*1000) FROM outbox WHERE id=? AND conv IS NULL`, id).Scan(&recipient, &r.toKey, &in.TS, &in.Kind, &in.Body, &in.ReplyTo, &in.Status, &in.Sub, &in.Origin, &target, &refID, &refFP, &in.AgentID, &at)
		in.From, key, r.to = a.Address, a.Self().Fingerprint(), recipient
	} else {
		return r, errors.New("device history: invalid source")
	}
	if err != nil {
		return r, err
	}
	if !protocol.ValidFingerprint(key) {
		return r, errors.Join(errDeviceHistoryBlocked, errors.New("original key unavailable"))
	}
	if target != "" {
		if err = json.Unmarshal([]byte(target), &in.Target); err != nil {
			return r, err
		}
	}
	if refID != "" {
		in.Ref = &envelope.Ref{ID: refID, Fingerprint: refFP}
	}
	var originalTo, originalKey string
	err = q.QueryRow(`SELECT recipient,recipient_fp FROM device_history_rows WHERE storage=? AND id=?`, storage, id).Scan(&originalTo, &originalKey)
	if err == nil {
		r.to, r.toKey = originalTo, originalKey
	} else if !errors.Is(err, sql.ErrNoRows) {
		return r, err
	}
	h, err := a.historySourceItem(q, historySourceRow{dir: storage, key: key, in: in, pos: historyPos{Ms: at, ID: id}})
	if err != nil {
		return r, err
	}
	r.item = h
	if err = validateDeviceHistoryItem(r); err != nil {
		return r, errors.Join(errDeviceHistoryBlocked, err)
	}
	return r, nil
}

func validateDeviceHistoryItem(r deviceHistoryRow) error {
	h := r.item
	if h.ReceiverRoute != nil && h.ReceiverRoute.Op != "request" {
		return errors.New("device history: receiver setup is not visible history")
	}
	if h.V != 1 || h.ID != h.LID || !protocol.ValidFingerprint(h.FromKey) || h.GroupHistory != nil || h.GroupAdmission != "" || h.PID != "" || h.Human != nil || h.Topic != "" || h.TopicEvent != nil || h.SendGroup != "" {
		return errors.New("device history: invalid original scope")
	}
	for _, f := range h.Attachments {
		if f.Size > MaxFileSize || f.Blob.ID != "" || f.Blob.Size != 0 || f.Blob.SHA256 != "" {
			return errors.New("device history: invalid original manifest")
		}
	}
	if _, _, e := protocol.SplitAddress(h.From); e != nil {
		return e
	}
	switch h.Sub {
	case "", envelope.SubStatus, envelope.SubReaction, envelope.SubRevision, envelope.SubRetraction:
	default:
		return errors.New("device history: non-display operation")
	}
	return envelope.ValidateDeviceHistory(deviceHistoryInner(r))
}

// Only verified stored roster steps prove an original own-human endpoint.
// They preserve old attribution, never restore a removed device's live rights.
func deviceHistoryHuman(q dbq, person, address, fp string) (bool, error) {
	if fp == "" {
		return false, nil
	}
	rows, err := q.Query(`SELECT record FROM person_chain WHERE person=?`, person)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var roster protocol.PersonRoster
		if err = rows.Scan(&raw); err != nil {
			return false, err
		}
		if json.Unmarshal(raw, &roster) == nil && roster.Has(address, fp) && roster.Human(fp) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (a *Agent) deviceHistoryProjection(q dbq, r *deviceHistoryRow, person string) error {
	fromOwn, err := deviceHistoryHuman(q, person, r.item.From, r.item.FromKey)
	if err != nil {
		return err
	}
	toOwn, err := deviceHistoryHuman(q, person, r.to, r.toKey)
	if err != nil {
		return err
	}
	if !fromOwn && !toOwn {
		return errors.Join(errDeviceHistoryBlocked, errors.New("neither original endpoint belongs to this human"))
	}
	r.peer, r.dir = r.item.From, "in"
	if fromOwn {
		r.peer, r.dir = r.to, "out"
	}
	ref := r.item.ReplyTo
	if r.item.Ref != nil {
		ref = r.item.Ref.ID
	}
	if ref != "" {
		parent, e := a.deviceHistoryOriginal(q, ref)
		if errors.Is(e, sql.ErrNoRows) && fromOwn && toOwn {
			return ErrGroupContextPending
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var peer, dir string
		err = q.QueryRow(`SELECT peer,direction FROM device_history_rows WHERE id=? AND storage=?`, ref, parent.storage).Scan(&peer, &dir)
		if err == nil {
			r.peer = peer
			if r.item.Kind == envelope.KindQuestion || r.item.Kind == envelope.KindTask {
				r.dir = dir
			}
			if r.item.Kind == envelope.KindAnswer || r.item.Kind == envelope.KindResult || r.item.Status == envelope.StatusProgress {
				if r.item.From == peer {
					r.dir = "in"
				} else {
					r.dir = "out"
				}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	return nil
}

func storeDeviceHistoryMetadata(q *sql.Tx, r deviceHistoryRow) error {
	_, err := q.Exec(`INSERT INTO device_history_rows(storage,id,recipient,recipient_fp,peer,direction,author_fp,hash,reply_to,at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(storage,id) DO UPDATE SET peer=excluded.peer,direction=excluded.direction`, r.storage, r.id, r.to, r.toKey, r.peer, r.dir, r.item.FromKey, deviceHistoryHash(r), r.item.ReplyTo, r.item.At/1000)
	return err
}

func (a *Agent) admitDeviceHistory(ctx context.Context, env envelope.Envelope, in envelope.Inner, sender identity.Public, held bool, hold func(string, string) error) error {
	w, err := protocol.ParseDeviceHistory([]byte(in.Body))
	if err != nil {
		return hold(reasonInvalid, err.Error())
	}
	r := deviceHistoryRow{storage: "in", to: w.Recipient, toKey: w.RecipientKey}
	if err = decodeStrict(w.Item, &r.item); err != nil {
		return hold(reasonInvalid, "device history: malformed original")
	}
	r.id = r.item.ID
	if err = validateDeviceHistoryItem(r); err != nil {
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
	auth := protocol.ReadSync{V: 1, Person: w.Person, Roster: w.Roster}
	if err = readSyncAuthority(tx, auth, env.From, sender.Fingerprint(), a.Address, a.Self().Fingerprint()); err != nil {
		tx.Rollback()
		return hold(reasonInvalid, err.Error())
	}
	var erasedHash string
	err = tx.QueryRow(`SELECT h.hash FROM device_history_rows h JOIN inbox i ON i.id=h.id JOIN conv_erased e ON e.conv='' AND e.key=h.author_fp AND e.lid=h.id WHERE h.storage='in' AND h.id=? AND h.author_fp=? AND h.recipient=?`, r.id, r.item.FromKey, r.to).Scan(&erasedHash)
	if err == nil {
		if erasedHash != deviceHistoryHash(r) {
			tx.Rollback()
			return hold(reasonConflict, "device history differs from an erased original")
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO history_receipts(id) VALUES(?)`, env.ID); err != nil {
			return err
		}
		if held {
			if _, err = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
				return err
			}
		}
		return a.store.done(tx.Commit())
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err = a.deviceHistoryProjection(tx, &r, w.Person); err != nil {
		tx.Rollback()
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, "device history awaits its exact original request")
		}
		return hold(reasonInvalid, err.Error())
	}
	if err = a.deviceHistoryBinding(tx, r); err != nil {
		tx.Rollback()
		if errors.Is(err, ErrGroupContextPending) {
			return hold(reasonProof, "device history awaits its exact original request")
		}
		return hold(reasonInvalid, err.Error())
	}
	// Native originals win. Conflicting originals never become "delivered".
	for _, storage := range []string{"out", "in"} {
		old, e := a.deviceHistorySource(tx, storage, r.id)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if old.item.FromKey != r.item.FromKey || deviceHistoryHash(old) != deviceHistoryHash(r) {
			tx.Rollback()
			return hold(reasonConflict, "device history differs from a stored original")
		}
		old.peer, old.dir = r.peer, r.dir
		if err = storeDeviceHistoryMetadata(tx, old); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO history_receipts(id) VALUES(?)`, env.ID); err != nil {
			return err
		}
		if held {
			if _, err = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
				return err
			}
		}
		return a.store.done(tx.Commit())
	}
	h := r.item
	refID, refFP := refCols(h.Ref)
	at := h.At
	if at <= 0 || at > time.Now().UnixMilli() {
		at = time.Now().UnixMilli()
	}
	r.item.At = at
	_, err = tx.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,reply_to,received_at,status,state,sub,replica,origin,target,content_hash,received_ms,claimed_fp,via,acked,ref_id,ref_fp,agent_id,quote,topic_done,receiver_route) VALUES(?,?,?,?,?,nullif(?,''),?,nullif(?,''),'',nullif(?,''),1,nullif(?,''),nullif(?,''),?,?,?, ?,1,?,?,nullif(?,''),nullif(?,''),?,nullif(?,''))`, h.ID, h.From, h.TS, h.Kind, h.Body, h.ReplyTo, at/1000, h.Status, h.Sub, h.Origin, targetJSON(h.Target), deviceHistoryHash(r), at, h.FromKey, env.From, refID, refFP, h.AgentID, h.Quote, h.TopicDone, receiverRouteJSON(h.ReceiverRoute))
	if err != nil {
		return err
	}
	if err = storeDeviceHistoryMetadata(tx, r); err != nil {
		return err
	}
	for i, f := range h.Attachments {
		if _, err = tx.Exec(`INSERT INTO attachments(message_id,blob_id,name,size,sha256,ct_size,ct_sha256) VALUES(?,?,?,?,?,0,'')`, h.ID, fmt.Sprintf("%s%d", historyBlob, i), f.Name, f.Size, f.SHA256); err != nil {
			return err
		}
	}
	if err = eraseArrivalIn(tx, h.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT OR IGNORE INTO history_receipts(id) VALUES(?)`, env.ID); err != nil {
		return err
	}
	if held {
		if _, err = tx.Exec(`DELETE FROM quarantine WHERE id=?`, env.ID); err != nil {
			return err
		}
	}
	if err = a.store.done(tx.Commit()); err == nil {
		a.convWork.due(convHistory | convRetry)
		a.kickNow()
	}
	return err
}
