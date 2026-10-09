package client

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// No authority backfill: old rows do not acquire today's admission. This is
// message provenance and the existing file queue, never a history archive.
const GroupHistorySchema = `
ALTER TABLE inbox ADD COLUMN group_admission TEXT;
ALTER TABLE outbox ADD COLUMN group_admission TEXT;
ALTER TABLE file_serves ADD COLUMN group_descriptor BLOB;
ALTER TABLE file_requests ADD COLUMN group_descriptor BLOB;
`

var ErrGroupHistoryUnavailable = errors.New("group: selected history changed or unavailable")

type GroupHistorySelection struct {
	Last  int
	Since int64 // unix milliseconds; zero means not selected
	Refs  []protocol.GroupHistoryRef
}

type groupHistorySource struct {
	item     HistoryItem
	dir      string
	stamp    string
	original bool // full immutable source, restricted to current own admission
}

func historyRef(conv string, item HistoryItem) protocol.GroupHistoryRef {
	return protocol.GroupHistoryRef{LID: item.LID, Author: item.FromKey, Hash: contentHash(item.inner(conv))}
}

func groupMemberAdmission(q dbq, packet GroupContext, address, fp string) (protocol.GroupAdmission, error) {
	if err := groupTurnCheck(q, packet, address, fp); err != nil {
		return protocol.GroupAdmission{}, err
	}
	for _, m := range packet.State.Members {
		p, ok, err := personByIDIn(q, m.Person)
		if err != nil {
			return protocol.GroupAdmission{}, err
		}
		if ok && p.has(address, fp) {
			return m.Admission, nil
		}
	}
	return protocol.GroupAdmission{}, ErrGroupContextPending
}

func groupHistorySelected(q dbq, packet GroupContext, address, fp string, ref protocol.GroupHistoryRef) error {
	admission, err := groupMemberAdmission(q, packet, address, fp)
	if err != nil {
		return err
	}
	if !admission.AllowsHistory(ref) {
		return errors.New("group: history is not in exact signed admission")
	}
	return nil
}

// Ordinary rows are the only source. Recipient-specific ciphertext and file
// blob keys never enter the selected hash. Copies of one authored logical turn
// must resolve to the same visible bytes; an arbitrary first candidate is unsafe.
func (a *Agent) groupHistorySources(q dbq, conv, lid, author string, since int64, limit int, original ...bool) ([]groupHistorySource, error) {
	rows, err := q.Query(`SELECT * FROM (
 SELECT id,'in',sender,coalesce(verified_by,claimed_fp,'') AS author,ts,body,coalesce(reply_to,''),coalesce(origin,''),coalesce(emotion,''),lid,coalesce(received_ms,received_at*1000) AS ms,coalesce(group_admission,'') FROM inbox WHERE conv=? AND kind='message' AND coalesce(sub,'')='' AND ref_id IS NULL AND target IS NULL AND pid IS NULL AND agent_id IS NULL AND local=0
 UNION ALL
 SELECT id,'out',?,?,created_at,body,coalesce(reply_to,''),coalesce(origin,''),coalesce(emotion,''),lid,coalesce(created_ms,created_at*1000),coalesce(group_admission,'') FROM outbox o WHERE conv=? AND kind='message' AND coalesce(sub,'')='' AND ref_id IS NULL AND target IS NULL AND pid IS NULL AND agent_id IS NULL AND o.rowid=(SELECT min(f.rowid) FROM outbox f WHERE f.conv=o.conv AND f.lid=o.lid AND coalesce(f.sub,'')=''))
 s WHERE (?='' OR lid=?) AND (?='' OR author=?) AND ms>=?
 AND NOT EXISTS(SELECT 1 FROM inbox c WHERE c.conv=? AND c.sub='retraction' AND c.ref_id=s.lid AND c.ref_fp=s.author)
 AND NOT EXISTS(SELECT 1 FROM outbox c WHERE c.conv=? AND c.sub='retraction' AND c.ref_id=s.lid AND c.ref_fp=s.author)
 AND NOT EXISTS(SELECT 1 FROM conv_erased e WHERE e.conv=? AND e.lid=s.lid AND e.key=s.author)
 ORDER BY ms DESC,id DESC LIMIT ?`, conv, a.Address, a.Self().Fingerprint(), conv, lid, lid, author, author, since, conv, conv, conv, limit)
	if err != nil {
		return nil, err
	}
	var result []groupHistorySource
	for rows.Next() {
		var s groupHistorySource
		s.item.V = 1
		s.item.Kind = envelope.KindMessage
		if err = rows.Scan(&s.item.ID, &s.dir, &s.item.From, &s.item.FromKey, &s.item.TS, &s.item.Body, &s.item.ReplyTo, &s.item.Origin, &s.item.Emotion, &s.item.LID, &s.item.At, &s.stamp); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	visible := result[:0]
	for _, s := range result {
		// Preserve the complete immutable source before optionally projecting edits
		// for a selected excerpt. Own-device sync sends edits as separate controls.
		rows, e := a.historySourceRows(q, "dir=? AND id=?", "conv,ms,id", 1, s.dir, s.item.ID)
		if e != nil {
			return nil, e
		}
		if len(rows) != 1 {
			continue
		}
		complete, e := a.historySourceItem(q, rows[0])
		if e != nil {
			return nil, e
		}
		if len(original) > 0 && original[0] {
			s.item = complete
			s.original = true
		} else {
			// Existing selected grants bind the legacy visible projection's
			// bytes. Do not add fields to an already signed selection.
			s.item.Attachments = complete.Attachments
			s.item.ReceiverRoute = complete.ReceiverRoute
		}
		if retractedRef(q, conv, s.item.LID, s.item.FromKey) {
			continue
		}
		if len(original) == 0 || !original[0] {
			// Stored revisions already passed the ordinary control authority boundary.
			revisions, e := q.Query(`SELECT body FROM inbox WHERE conv=? AND ref_id=? AND ref_fp=? AND sub='revision' UNION ALL SELECT body FROM outbox WHERE conv=? AND ref_id=? AND ref_fp=? AND sub='revision'`, conv, s.item.LID, s.item.FromKey, conv, s.item.LID, s.item.FromKey)
			if e != nil {
				return nil, e
			}
			var rev int64
			for revisions.Next() {
				var raw string
				var r envelope.Revision
				if e = revisions.Scan(&raw); e != nil {
					break
				}
				if json.Unmarshal([]byte(raw), &r) == nil && r.Rev > rev {
					rev = r.Rev
					s.item.Body = r.Text
				}
			}
			if e == nil {
				e = revisions.Err()
			}
			revisions.Close()
			if e != nil {
				return nil, e
			}
		}
		if !ordinaryGroupTurn(s.item.inner(conv)) {
			continue
		}
		visible = append(visible, s)
	}
	return visible, nil
}

func (a *Agent) groupHistorySourceIn(q dbq, conv string, ref protocol.GroupHistoryRef, original ...bool) (groupHistorySource, error) {
	sources, err := a.groupHistorySources(q, conv, ref.LID, ref.Author, 0, 3, original...)
	if err != nil {
		return groupHistorySource{}, err
	}
	if len(sources) == 0 {
		return groupHistorySource{}, ErrGroupHistoryUnavailable
	}
	for _, s := range sources {
		if historyRef(conv, s.item) != ref {
			return groupHistorySource{}, ErrGroupHistoryUnavailable
		}
	}
	return sources[0], nil
}

func (a *Agent) groupHistorySelectionIn(q dbq, conv string, refs []protocol.GroupHistoryRef) ([]HistoryItem, error) {
	if len(refs) > protocol.MaxGroupHistory {
		return nil, errors.New("group: history exceeds64 turns")
	}
	var items []HistoryItem
	seen := map[string]bool{}
	for _, ref := range refs {
		key := ref.Author + ":" + ref.LID
		if !protocol.ValidID(ref.LID) || !protocol.ValidFingerprint(ref.Author) || !protocol.ValidHash(ref.Hash) || seen[key] {
			return nil, errors.New("group: invalid or duplicate selected history")
		}
		seen[key] = true
		source, err := a.groupHistorySourceIn(q, conv, ref)
		if err != nil {
			return nil, err
		}
		items = append(items, source.item)
	}
	return items, nil
}

// SelectGroupHistory resolves a user selection against actual visible turns.
// Nothing is the default; selectors never implicitly expand beyond64 messages.
func (a *Agent) SelectGroupHistory(ctx context.Context, conv string, s GroupHistorySelection) ([]protocol.GroupHistoryRef, error) {
	packet, err := a.GroupContext(conv)
	if err != nil {
		return nil, err
	}
	if err = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return nil, err
	}
	n := 0
	if s.Last != 0 {
		n++
	}
	if s.Since != 0 {
		n++
	}
	if s.Refs != nil {
		n++
	}
	if n > 1 || s.Last < 0 || s.Last > protocol.MaxGroupHistory || s.Since < 0 {
		return nil, errors.New("group: select exactly one history mode, bounded64")
	}
	if s.Refs != nil {
		_, err = a.groupHistorySelectionIn(a.store.db, conv, s.Refs)
		return slices.Clone(s.Refs), err
	}
	if n == 0 {
		return nil, nil
	}
	sources, err := a.groupHistorySources(a.store.db, conv, "", "", s.Since, protocol.MaxGroupHistory+1)
	if err != nil {
		return nil, err
	}
	if s.Last > 0 && len(sources) > s.Last {
		sources = sources[:s.Last]
	}
	if len(sources) > protocol.MaxGroupHistory {
		return nil, errors.New("group: selected history exceeds64; select fewer turns")
	}
	slices.Reverse(sources)
	refs := make([]protocol.GroupHistoryRef, 0, len(sources))
	for _, source := range sources {
		refs = append(refs, historyRef(conv, source.item))
	}
	return refs, nil
}

func (a *Agent) groupHistoryCarrier(to identity.Public, packet GroupContext, item HistoryItem) (outCopy, error) {
	recipient, err := to.Recipient()
	if err != nil {
		return outCopy{}, err
	}
	raw, _ := json.Marshal(packet.Root)
	body, _ := json.Marshal(item)
	in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: a.Address, To: to.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: string(body), Conv: packet.State.Conv, LID: protocol.NewID(), Root: raw, Replica: true, Sub: envelope.SubHistory}
	env, err := envelope.Seal(in, a.id.Sign, recipient)
	return outCopy{env: env, in: in, state: stateQueued, required: protocol.CapGroup, recipientFP: to.Fingerprint()}, err
}

func groupHistoryCollision(q dbq, conv string, item HistoryItem) error {
	var storedConv, hash string
	err := q.QueryRow(`SELECT conv,content_hash FROM inbox WHERE coalesce(verified_by,claimed_fp)=? AND lid=?`, item.FromKey, item.LID).Scan(&storedConv, &hash)
	if err == nil && (storedConv != conv || hash != historyRef(conv, item).Hash) {
		return errors.New("group: conflicting history logical identity")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var lid, fp, c string
	err = q.QueryRow(`SELECT conv,lid,coalesce(verified_by,claimed_fp,'') FROM inbox WHERE id=?`, item.ID).Scan(&c, &lid, &fp)
	if err == nil && (c != conv || lid != item.LID || fp != item.FromKey) {
		return fmt.Errorf("group: conflicting history physical identity")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return nil
}

func (a *Agent) groupHistoryOutboundCheck(q dbq, packet GroupContext, to, fp string, item HistoryItem) error {
	if err := groupTurnCheck(q, packet, a.Address, a.Self().Fingerprint()); err != nil {
		return err
	}
	admission, err := groupMemberAdmission(q, packet, to, fp)
	if err != nil {
		return err
	}
	ref := historyRef(packet.State.Conv, item)
	original := false
	if !admission.AllowsHistory(ref) {
		me, ok, e := scanPersonIn(q, "state = ?", personSelf)
		if e != nil {
			return e
		}
		if !ok || !me.has(to, fp) || item.GroupAdmission == "" || item.GroupAdmission != admission.Hash() {
			return errors.New("group: history recipient lacks signed selection/current own live stamp")
		}
		// The stored live-admission stamp establishes this person's access
		// when the message arrived. The original author may since have left.
		// Exact source resolution below binds the claimed author and content;
		// only the forwarding and receiving devices need current membership.
		original = true
	}
	source, err := a.groupHistorySourceIn(q, packet.State.Conv, ref, original)
	if err != nil {
		return err
	}
	if item.GroupAdmission != "" && source.stamp != item.GroupAdmission {
		return ErrGroupHistoryUnavailable
	}
	return nil
}

// Invoked by the existing exact ordinary-message redaction path, including
// while the daemon is stopped. No snapshot text survives inside a carrier.
func (a *Agent) redactGroupHistoryCopies(ref ControlRef) {
	if ref.Conv == "" {
		return
	}
	rows, err := a.store.db.Query(`SELECT id,envelope,state FROM outbox WHERE conv=? AND required_cap=? AND ((sub='history' AND json_valid(body) AND ((json_extract(body,'$.lid')=? AND json_extract(body,'$.from_key')=?) OR (json_extract(body,'$.sub') IN ('reaction','revision','retraction') AND json_extract(body,'$.ref.id')=? AND json_extract(body,'$.ref.fingerprint')=?))) OR (sub='file' AND json_valid(body) AND json_extract(body,'$.lid')=? AND json_extract(body,'$.author')=?))`, ref.Conv, protocol.CapGroup, ref.ID, ref.Fingerprint, ref.ID, ref.Fingerprint, ref.ID, ref.Fingerprint)
	if err != nil {
		a.Logf("redacting group history carriers: %v", err)
		return
	}
	type copy struct {
		id    string
		env   envelope.Envelope
		state string
	}
	var copies []copy
	for rows.Next() {
		var c copy
		var raw []byte
		if err = rows.Scan(&c.id, &raw, &c.state); err != nil {
			break
		}
		if json.Unmarshal(raw, &c.env) == nil {
			copies = append(copies, c)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		a.Logf("redacting group history carriers: %v", err)
		return
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		a.Logf("redacting group history carriers: %v", err)
		return
	}
	defer tx.Rollback()
	for _, c := range copies {
		_, err = tx.Exec(`UPDATE outbox SET body='',state=CASE WHEN state IN('queued','waiting','failed') THEN ? ELSE state END,error=CASE WHEN state IN('queued','waiting','failed') THEN 'selected history retracted' ELSE error END WHERE id=?`, stateNotDelivered, c.id)
		if err != nil {
			a.Logf("redacting group history carriers: %v", err)
			return
		}
	}
	if _, err = tx.Exec(`UPDATE file_serves SET state='unavailable',updated_at=? WHERE conv=? AND state='pending' AND json_valid(group_descriptor) AND json_extract(group_descriptor,'$.message.lid')=? AND json_extract(group_descriptor,'$.message.author')=?`, time.Now().Unix(), ref.Conv, ref.ID, ref.Fingerprint); err != nil {
		a.Logf("redacting group history file serves: %v", err)
		return
	}
	if err = tx.Commit(); err != nil {
		a.Logf("redacting group history carriers: %v", err)
		return
	}
	// releaseSpool has no lock acquisition; the caller may already own the
	// ordinary spool lock. Only this exact carrier's ciphertext is released.
	for _, c := range copies {
		if c.state == stateQueued || c.state == stateConvWaiting || c.state == stateFailed {
			a.releaseSpool(c.env)
		}
	}
}
