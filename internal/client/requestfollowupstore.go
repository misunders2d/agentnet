package client

import (
	"database/sql"
	"encoding/json"
	"strings"

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
 AND earlier.state IN ('pending','accepted','awaiting','running','cancel_requested','` + stateAgentWaiting + `') AND earlier.id<>inbox.id
 AND (( (earlier.id=json_extract(inbox.request_followup,'$.id') OR earlier.lid=json_extract(inbox.request_followup,'$.id'))
        AND earlier.verified_by=json_extract(inbox.request_followup,'$.fingerprint'))
   OR (earlier.arrival<inbox.arrival AND earlier.request_followup=inbox.request_followup
       AND earlier.kind=inbox.kind AND coalesce(earlier.pid,'')=coalesce(inbox.pid,'') AND coalesce(earlier.topic,'')=coalesce(inbox.topic,'') AND coalesce(earlier.target,'')=coalesce(inbox.target,'')
       AND (earlier.verified_by=inbox.verified_by AND earlier.sender=inbox.sender
         OR earlier.verified_by=json_extract(inbox.request_followup,'$.fingerprint')
         OR EXISTS(SELECT 1 FROM person_devices current JOIN person_devices previous ON previous.person=current.person JOIN persons p ON p.person=current.person
           WHERE current.address=inbox.sender AND current.fingerprint=inbox.verified_by AND previous.address=earlier.sender AND previous.fingerprint=earlier.verified_by AND p.state<>'conflict'
           AND EXISTS(SELECT 1 FROM json_each(p.record,'$.human_keys') h WHERE h.value=previous.fingerprint)))))))`

// Evaluate correction ordering only for marked candidates in the same claim
// transaction. Ordinary pages avoid repeatedly planning the correlated proof
// query; no authority or row can change between this check and the claim.
func requestFollowupReadyIn(q dbq, id string) (bool, error) {
	var ready bool
	err := q.QueryRow(`SELECT `+requestFollowupReady+` FROM inbox WHERE id=?`, id).Scan(&ready)
	return ready, err
}

// Called only after a successful, normally authorized claim, in its transaction.
// A signed link settles its exact prior clarification, never another card or
// an unknown historical execution. The new claim still uses native permissions.
func settleRequestFollowupIn(tx *sql.Tx, j job) error {
	if j.Kind != envelope.KindQuestion && j.Kind != envelope.KindTask {
		return nil
	}
	ref, err := storedRequestFollowup(tx, "in", j.ID)
	if err != nil || ref == nil || ref.Attempt != 0 {
		return err
	}
	lookup := ref.ID
	if j.ReplyTo != "" {
		lookup = j.ReplyTo // a later round may answer a correction linked to the same root
	}
	var candidates []struct {
		id, lid, from, key, kind, body, pid, topic, target, origin, state string
	}
	rows, err := tx.Query(`SELECT id,coalesce(nullif(lid,''),id),sender,verified_by,kind,body,coalesce(pid,''),coalesce(topic,''),coalesce(target,''),coalesce(origin,''),state
		FROM inbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND replica=0 AND coalesce(sub,'')='' AND body<>''
		AND (verified_by=? AND (id=? OR lid=?) OR json_extract(request_followup,'$.id')=? AND json_extract(request_followup,'$.fingerprint')=?)
		AND receiver_route IS NULL AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs x WHERE x.inbox_id=inbox.id) LIMIT 2`, j.Conv, lookup, lookup, ref.Fingerprint, ref.ID, ref.ID, ref.ID, ref.Fingerprint)
	if err != nil {
		return err
	}
	for rows.Next() {
		var c struct{ id, lid, from, key, kind, body, pid, topic, target, origin, state string }
		if err = rows.Scan(&c.id, &c.lid, &c.from, &c.key, &c.kind, &c.body, &c.pid, &c.topic, &c.target, &c.origin, &c.state); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(candidates) != 1 {
		return err
	}
	c := candidates[0]
	var topic, self string
	if err = tx.QueryRow(`SELECT coalesce(topic,'') FROM inbox WHERE id=?`, j.ID).Scan(&topic); err != nil {
		return err
	}
	if c.state != stateNeedHuman || c.kind != j.Kind || c.pid != j.PID || c.topic != topic || c.target != targetJSON(j.Target) || envelope.AgentOrigin(c.origin) {
		return nil
	}
	if err = tx.QueryRow(`SELECT v FROM config WHERE k='address'`).Scan(&self); err != nil {
		return err
	}
	var rootFrom, rootLID, rootOrigin string
	var roots int
	var rootMatches bool
	if err = tx.QueryRow(`SELECT coalesce(min(sender),''),coalesce(min(nullif(lid,'')),min(id),''),coalesce(min(origin),''),count(*),coalesce(min(kind=? AND coalesce(pid,'')=? AND coalesce(topic,'')=? AND coalesce(target,'')=? AND coalesce(sub,'')='' AND body<>''),0)
		FROM inbox WHERE coalesce(conv,'')=? AND (id=? OR lid=?) AND verified_by=? AND replica=0`, j.Kind, j.PID, topic, targetJSON(j.Target), j.Conv, ref.ID, ref.ID, ref.Fingerprint).Scan(&rootFrom, &rootLID, &rootOrigin, &roots, &rootMatches); err != nil || roots != 1 || !rootMatches || envelope.AgentOrigin(rootOrigin) {
		return err
	}
	if ok, e := followupHumanIn(tx, self, "", j.From, j.Key, HistoryItem{From: rootFrom, FromKey: ref.Fingerprint}); e != nil || !ok {
		return e
	}
	// Without a current own-person roster, the store cannot independently
	// prove a legacy self key. Preserve that card rather than guess an identity.
	ok, err := followupHumanIn(tx, self, "", j.From, j.Key, HistoryItem{From: c.from, FromKey: c.key})
	if err != nil || !ok {
		return err
	}
	var erased bool
	if err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM conv_erased WHERE conv=? AND (key=? AND lid=? OR key=? AND lid=?))`, j.Conv, c.key, c.lid, ref.Fingerprint, rootLID).Scan(&erased); err != nil || erased {
		return err
	}
	_, err = tx.Exec(`UPDATE inbox SET state=?,read_at=coalesce(read_at,unixepoch()),status_due=status_due+1 WHERE id=? AND state=?`, stateResolved, c.id, stateNeedHuman)
	return err
}

func requestFollowupJSON(ref *envelope.FollowupRef) string {
	if ref == nil {
		return ""
	}
	b, _ := json.Marshal(ref)
	return string(b)
}

func storeRequestFollowup(tx *sql.Tx, table, id string, ref *envelope.FollowupRef) error {
	if ref == nil {
		return nil
	}
	_, err := tx.Exec("UPDATE "+table+" SET request_followup=? WHERE id=?", requestFollowupJSON(ref), id)
	return err
}

func storedRequestFollowup(q dbq, dir, id string) (*envelope.FollowupRef, error) {
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
	var ref envelope.FollowupRef
	if err := json.Unmarshal([]byte(raw.String), &ref); err != nil {
		return nil, err
	}
	return &ref, nil
}

// Supplemental to group/receiver capabilities: retained history must not lose
// explicit intent when sent to a reader predating the launch-time guard.
func carriedFollowup(sub, body string) (*envelope.FollowupRef, error) {
	if body == "" {
		return nil, nil
	}
	raw := []byte(body)
	if sub == envelope.SubDeviceHistory {
		packet, err := protocol.ParseDeviceHistory(raw)
		if err != nil {
			return nil, err
		}
		raw = packet.Item
	}
	if sub == envelope.SubHistory || sub == envelope.SubDeviceHistory {
		if body == "" {
			return nil, nil
		}
		var item HistoryItem
		if err := json.Unmarshal(raw, &item); err != nil {
			return nil, err
		}
		return item.Followup, nil
	}
	var setup envelope.ReceiverOperation
	if json.Unmarshal(raw, &setup) == nil && setup.Request != nil {
		return setup.Request.Followup, nil
	}
	return nil, nil
}
func requestFollowupCopyRef(q dbq, id, sub, body string) (*envelope.FollowupRef, error) {
	if sub == envelope.SubHistory || sub == envelope.SubDeviceHistory {
		return carriedFollowup(sub, body)
	}
	ref, err := storedRequestFollowup(q, "out", id)
	if err != nil || ref != nil {
		return ref, err
	}
	var receiver bool
	if err = q.QueryRow(`SELECT coalesce(required_cap,'')=? AND reply_receiver IS NULL FROM outbox WHERE id=?`, protocol.CapReplyReceiver, id).Scan(&receiver); err != nil || !receiver {
		return nil, err
	}
	return carriedFollowup(sub, body)
}
func requestFollowupCopy(q dbq, id, sub, body string) (bool, error) {
	ref, err := requestFollowupCopyRef(q, id, sub, body)
	return ref != nil, err
}
func requestSteerCopy(q dbq, id, sub, body string) (bool, error) {
	ref, err := requestFollowupCopyRef(q, id, sub, body)
	return ref != nil && ref.Attempt > 0, err
}

func steerRequirement(required string, needed bool) string {
	if !needed {
		return required
	}
	if _, present := splitSteerRequirement(required); present {
		return required
	}
	if required == "" {
		return protocol.CapRequestSteer
	}
	return required + "+" + protocol.CapRequestSteer
}
func splitSteerRequirement(required string) (string, bool) {
	if required == protocol.CapRequestSteer {
		return "", true
	}
	base := strings.TrimSuffix(required, "+"+protocol.CapRequestSteer)
	return base, base != required
}
