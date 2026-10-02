package client

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func (a *Agent) importedReceiverControl(in envelope.Inner, fp string) (bool, error) {
	if in.Conv != "" || in.Sub != envelope.SubRetraction || in.Ref == nil || in.Ref.Fingerprint != fp {
		return false, nil
	}
	originals, err := importedReceiverOriginals(a.store.db, envelope.Inner{ReplyTo: in.Ref.ID})
	if err != nil {
		return false, err
	}
	if len(originals) != 1 {
		return false, nil
	}
	r := originals[0].remote
	if r.Request.ID != in.Ref.ID || r.Request.Conv != "" || r.Request.From != in.From || r.Request.FromKey != fp || r.Route.Host != a.Address || r.Route.HostKey != a.Self().Fingerprint() {
		return false, nil
	}
	if _, err = replyReceiverHostIn(a.store.db, ReplyReceiverHost{Address: in.From, Fingerprint: fp}); err != nil {
		return false, err
	}
	return true, nil // retained accepted proof confers only exact deletion authority
}
func receiverRetracted(q dbq, r *receiverRemoteState) bool {
	return r != nil && r.Request.Kind == envelope.KindMessage && (r.Redacted || retractedRef(q, r.Request.Conv, r.Route.RequestRef, r.Request.FromKey))
}
func receiverInputRetraction(q dbq, b ReplyReceiverBinding, id string) error {
	if !receiverRetracted(q, b.remote) {
		return nil
	}
	var state string
	var claim sql.NullString
	if err := q.QueryRow(`SELECT state,live_claim FROM reply_receiver_inputs WHERE binding=? AND inbox_id=?`, b.ID, id).Scan(&state, &claim); err != nil {
		return err
	}
	if state == "accepted" || state == "completed" {
		return nil
	} // deletion never recalls accepted effects
	return errors.New("original ordinary request was deleted; selected input remains unclaimed")
}

func (a *Agent) receiverRetractionHost(q dbq, ref ControlRef) (*ReplyReceiverHost, error) {
	if ref.Conv != "" || ref.Fingerprint != a.Self().Fingerprint() {
		return nil, nil
	}
	b, _, err := receiverOriginCopy(q, ref.ID)
	if err != nil || b == nil {
		return nil, err
	}
	r := b.remote
	if r.Request.Conv != "" || r.Request.ID != ref.ID || r.Request.From != a.Address || r.Request.FromKey != ref.Fingerprint {
		return nil, errors.New("retraction differs from exact delegated original")
	}
	host := ReplyReceiverHost{Address: r.Route.Host, Fingerprint: r.Route.HostKey}
	if _, err = replyReceiverHostIn(q, host); err != nil {
		return nil, err
	}
	return &host, nil
}

// Local approval has already proved the original. Only its exact author may
// now release an earlier signed deletion held for that missing original.
func (a *Agent) importHeldReceiverRetractions(tx *sql.Tx, r *receiverRemoteState) (bool, error) {
	if r.Request.Conv != "" {
		return false, nil
	}
	key, err := replyReceiverHostIn(tx, ReplyReceiverHost{Address: r.Request.From, Fingerprint: r.Request.FromKey})
	if err != nil {
		return false, err
	}
	rows, err := tx.Query(`SELECT envelope FROM quarantine WHERE sender=? AND reason=? ORDER BY received_at,id`, r.Request.From, reasonProof)
	if err != nil {
		return false, err
	}
	var held []envelope.Envelope
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			break
		}
		var env envelope.Envelope
		if json.Unmarshal([]byte(raw), &env) == nil && env.V == envelope.Version3 && env.From == key.Address && env.To == a.Address {
			held = append(held, env)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	deleted := false
	for _, env := range held {
		in, e := envelope.Open(env, a.id, a.Address, key)
		if e != nil || in.Sub != envelope.SubRetraction || in.Conv != "" || in.Ref == nil || in.Ref.ID != r.Request.ID || in.Ref.Fingerprint != r.Request.FromKey || in.From != r.Request.From {
			continue
		}
		if e = addControlInboxIn(tx, in, key.Fingerprint(), true); e != nil {
			return false, e
		}
		deleted = true
	}
	return deleted && r.Request.Kind == envelope.KindMessage, nil
}

// Called only by the existing authorized exact-scope retraction path. The
// transaction fences ready/import and keeps every accepted/uncertain claim.
func (a *Agent) redactReceiverDelegations(ref ControlRef) {
	tx, err := a.store.db.Begin()
	if err != nil {
		return
	}
	defer tx.Rollback()
	if err = redactReceiverHistory(tx, ref); err != nil {
		return
	}
	rows, err := tx.Query(`SELECT id,receiver FROM reply_receivers`)
	if err != nil {
		return
	}
	var matches []ReplyReceiverBinding
	for rows.Next() {
		var b ReplyReceiverBinding
		var raw string
		if rows.Scan(&b.ID, &raw) != nil {
			rows.Close()
			return
		}
		if decodeReceiverBinding(raw, &b) != nil {
			rows.Close()
			return
		}
		r := b.remote
		if r != nil && r.Request.Kind == envelope.KindMessage && r.Request.Conv == ref.Conv && r.Route.RequestRef == ref.ID && r.Request.FromKey == ref.Fingerprint {
			matches = append(matches, b)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return
	}
	rows.Close()
	var blobs []string
	for _, candidate := range matches {
		b, e := replyReceiverIn(tx, candidate.ID)
		if e != nil {
			return
		}
		r := b.remote
		r.Redacted = true
		r.Request.Body = ""
		r.Request.Attachments = nil
		var accepted int
		if e = tx.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE binding=? AND (state='accepted' OR live_claim IS NOT NULL)`, b.ID).Scan(&accepted); e != nil {
			return
		}
		if accepted == 0 {
			if _, e = tx.Exec(`UPDATE reply_receivers SET canceled_at=coalesce(canceled_at,?) WHERE id=?`, time.Now().Unix(), b.ID); e != nil {
				return
			}
		}
		raw, e := encodeReceiverBinding(b)
		if e != nil {
			return
		}
		if _, e = tx.Exec(`UPDATE reply_receivers SET receiver=? WHERE id=?`, string(raw), b.ID); e != nil {
			return
		}
		for _, id := range r.OriginalIDs {
			if _, e = tx.Exec(`UPDATE outbox SET state=?,body='',error='original ordinary request was deleted before handover' WHERE id=? AND state=?`, stateNotDelivered, id, stateReceiverWaiting); e != nil {
				return
			}
		}
		// The exact internal wrapper is an additional stored copy of ordinary
		// text. Its transport kind does not change original message retention.
		if _, e = tx.Exec(`UPDATE outbox SET body='',state=CASE WHEN state IN (?,?) THEN ? ELSE state END WHERE id=?`, stateQueued, stateReceiverWaiting, stateNotDelivered, r.Route.DelegationID); e != nil {
			return
		}
		if _, e = tx.Exec(`UPDATE inbox SET body='',state=CASE WHEN state IN (?,?,?) THEN ? ELSE state END,detail='original ordinary delegation was deleted' WHERE id=?`, stateAwaiting, statePending, stateAccepted, stateNotRun, r.Route.DelegationID); e != nil {
			return
		}
		fileRows, e := tx.Query(`SELECT blob_id FROM attachments WHERE message_id=?`, r.Route.DelegationID)
		if e != nil {
			return
		}
		for fileRows.Next() {
			var blob string
			if fileRows.Scan(&blob) != nil {
				fileRows.Close()
				return
			}
			blobs = append(blobs, blob)
		}
		e = fileRows.Err()
		fileRows.Close()
		if e != nil {
			return
		}
		if _, e = tx.Exec(`DELETE FROM attachments WHERE message_id=?`, r.Route.DelegationID); e != nil {
			return
		}
		if _, e = tx.Exec(`UPDATE inbox SET state=?,detail='original ordinary request was deleted; no selected dispatch' WHERE id IN (SELECT inbox_id FROM reply_receiver_inputs WHERE binding=? AND state='pending' AND live_claim IS NULL)`, stateNotRun, b.ID); e != nil {
			return
		}
	}
	if tx.Commit() != nil {
		return
	}
	for _, blob := range blobs {
		os.Remove(a.downloadPath(blob))
		os.Remove(a.downloadPath(blob) + ".part")
	}
}

// Routed history retains a local typed body for capability checks. Delete
// only the exact ordinary original's copy, preserving Q/T retention and
// ciphertext already in custody. This uses the authorized control scope.
func redactReceiverHistory(tx *sql.Tx, ref ControlRef) error {
	rows, err := tx.Query(`SELECT id,body FROM outbox WHERE conv=? AND sub=? AND body<>''`, ref.Conv, envelope.SubHistory)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id, raw string
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		var h HistoryItem
		if json.Unmarshal([]byte(raw), &h) == nil && h.ReceiverRoute != nil && h.ReceiverRoute.Op == "request" && h.Kind == envelope.KindMessage && h.FromKey == ref.Fingerprint && (h.LID == ref.ID || h.LID == "" && h.ID == ref.ID) {
			ids = append(ids, id)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = tx.Exec(`UPDATE outbox SET body='',state=CASE WHEN state IN (?,?,?) THEN ? ELSE state END,error='original ordinary history was deleted' WHERE id=?`, stateQueued, stateConvWaiting, stateFailed, stateNotDelivered, id); err != nil {
			return err
		}
	}
	return nil
}
