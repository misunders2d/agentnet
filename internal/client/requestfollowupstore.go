package client

import (
	"database/sql"
	"encoding/json"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const requestFollowupSchema = `
ALTER TABLE inbox ADD COLUMN request_followup TEXT;
ALTER TABLE outbox ADD COLUMN request_followup TEXT;
CREATE INDEX inbox_request_followup ON inbox(json_extract(request_followup,'$.id'),json_extract(request_followup,'$.fingerprint'),arrival) WHERE request_followup IS NOT NULL;
`

// Preserve durable correction order even when legacy device requests share a
// received_at second and their random IDs sort the other way. Unmarked rows
// retain their existing scheduler and authority rules. Another person's forged
// marker cannot hold a human's queue: prior corrections must be from this key,
// the original key, or a current human key of this same verified person.
const requestFollowupReady = `(inbox.request_followup IS NULL OR NOT EXISTS (
 SELECT 1 FROM inbox earlier WHERE earlier.replica=0 AND coalesce(earlier.conv,'')=coalesce(inbox.conv,'')
 AND earlier.state IN ('pending','accepted','awaiting','running','cancel_requested','needs_human','` + stateAgentWaiting + `') AND earlier.id<>inbox.id
 AND (( (earlier.id=json_extract(inbox.request_followup,'$.id') OR earlier.lid=json_extract(inbox.request_followup,'$.id'))
        AND earlier.verified_by=json_extract(inbox.request_followup,'$.fingerprint'))
   OR (earlier.arrival<inbox.arrival AND earlier.request_followup=inbox.request_followup
       AND earlier.kind=inbox.kind AND coalesce(earlier.pid,'')=coalesce(inbox.pid,'') AND coalesce(earlier.topic,'')=coalesce(inbox.topic,'') AND coalesce(earlier.target,'')=coalesce(inbox.target,'')
       AND (earlier.verified_by=inbox.verified_by AND earlier.sender=inbox.sender
         OR earlier.verified_by=json_extract(inbox.request_followup,'$.fingerprint')
         OR EXISTS(SELECT 1 FROM person_devices current JOIN person_devices previous ON previous.person=current.person JOIN persons p ON p.person=current.person
           WHERE current.address=inbox.sender AND current.fingerprint=inbox.verified_by AND previous.address=earlier.sender AND previous.fingerprint=earlier.verified_by AND p.state<>'conflict'
           AND EXISTS(SELECT 1 FROM json_each(p.record,'$.human_keys') h WHERE h.value=previous.fingerprint)))))))`

func requestFollowupJSON(ref *envelope.Ref) string {
	if ref == nil {
		return ""
	}
	b, _ := json.Marshal(ref)
	return string(b)
}

func storeRequestFollowup(tx *sql.Tx, table, id string, ref *envelope.Ref) error {
	if ref == nil {
		return nil
	}
	_, err := tx.Exec("UPDATE "+table+" SET request_followup=? WHERE id=?", requestFollowupJSON(ref), id)
	return err
}

func storedRequestFollowup(q dbq, dir, id string) (*envelope.Ref, error) {
	table := "inbox"
	if dir == "out" {
		table = "outbox"
	}
	var raw sql.NullString
	if err := q.QueryRow("SELECT request_followup FROM "+table+" WHERE id=?", id).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, nil
	}
	var ref envelope.Ref
	if err := json.Unmarshal([]byte(raw.String), &ref); err != nil {
		return nil, err
	}
	return &ref, nil
}

// Supplemental to group/receiver capabilities: retained history must not lose
// explicit intent when sent to a reader predating the launch-time guard.
func requestFollowupCopy(q dbq, id, sub, body string) (bool, error) {
	if sub == envelope.SubHistory || sub == envelope.SubDeviceHistory {
		if body == "" {
			return false, nil // legacy copies retained no parsed body
		}
		raw := []byte(body)
		if sub == envelope.SubDeviceHistory {
			packet, err := protocol.ParseDeviceHistory(raw)
			if err != nil {
				return false, err
			}
			raw = packet.Item
		}
		var item HistoryItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return false, err
		}
		return item.Followup != nil, nil
	}
	ref, err := storedRequestFollowup(q, "out", id)
	if err != nil || ref != nil {
		return ref != nil, err
	}
	// A selected receiver's private delegation retains the same immutable
	// request snapshot. Its older reader must not silently drop this intent.
	var receiver bool
	if err = q.QueryRow(`SELECT coalesce(required_cap,'')=? AND reply_receiver IS NULL FROM outbox WHERE id=?`, protocol.CapReplyReceiver, id).Scan(&receiver); err != nil || !receiver {
		return false, err
	}
	var setup envelope.ReceiverOperation
	if err = json.Unmarshal([]byte(body), &setup); err != nil {
		return false, err
	}
	return setup.Request != nil && setup.Request.Followup != nil, nil
}
