package client

import (
	"encoding/json"
	"errors"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type historySourceRow struct {
	conv, dir, key  string
	in              envelope.Inner
	pos             historyPos
	arrival, outseq int64
}

// Both snapshot versions use exactly the same accepted original rows. Local
// execution stamps never become signed author identities. The inbox arrival
// counter is independent of a forwarded message's older conversation timestamp.
func (a *Agent) historySourceRows(q dbq, filter, order string, limit int, args ...any) ([]historySourceRow, error) {
	query := `
		SELECT conv, ms, id, dir, sender, coalesce(verified_by, claimed_fp, '') AS verified_by, ts, kind, body, coalesce(reply_to, '') AS reply_to, coalesce(status, '') AS status, coalesce(sub, '') AS sub,
		       coalesce(origin, '') AS origin, coalesce(emotion, '') AS emotion, coalesce(target, '') AS target, coalesce(pid, '') AS pid, lid, coalesce(ref_id, '') AS ref_id, coalesce(ref_fp, '') AS ref_fp, coalesce(agent_id, '') AS agent_id,coalesce(quote,'') AS quote,coalesce(topic,'') AS topic,coalesce(topic_event,'') AS topic_event,topic_done,arrival,outseq FROM (
		  SELECT conv, received_ms AS ms, id, 'in' AS dir, sender, verified_by, claimed_fp, ts, kind, body, reply_to, status, sub, origin, emotion, target, pid, lid, ref_id, ref_fp,
		         CASE WHEN kind IN ('question', 'task') THEN '' ELSE agent_id END AS agent_id,quote,topic,topic_event,topic_done,arrival,0 AS outseq
		    FROM inbox WHERE conv IS NOT NULL AND local = 0 AND coalesce(sub, '') NOT IN ('root-sync', 'history', 'clear', 'group-proof', 'group-context', 'group-invite', 'group-consent', 'group-withdrawal')
		     AND NOT ` + erasedInFor("inbox") + `
		  UNION ALL
		  SELECT o.conv, o.created_ms, o.id, 'out', ?, ?, NULL, CASE WHEN coalesce(o.topic,'') <> '' OR (coalesce(o.pid,'') <> '' OR o.sub IN ('status','reaction','revision','retraction')) AND o.conv IN (SELECT conv FROM group_context) THEN json_extract(o.envelope,'$.ts') ELSE o.created_at END, o.kind, o.body, o.reply_to, o.status, o.sub, o.origin, o.emotion, o.target, o.pid, o.lid, o.ref_id, o.ref_fp, o.agent_id,o.quote,o.topic,o.topic_event,o.topic_done,0,o.rowid
		    FROM outbox o WHERE o.conv IS NOT NULL AND coalesce(o.sub, '') NOT IN ('root-sync', 'history', 'file', 'clear', 'group-proof', 'group-context', 'group-invite', 'group-consent', 'group-withdrawal')
		     AND o.rowid = (SELECT min(rowid) FROM outbox f WHERE f.conv = o.conv AND f.lid = o.lid) AND NOT ` + erasedOut + `) WHERE conv IN (SELECT id FROM conversations UNION SELECT conv FROM group_context UNION SELECT conv FROM group_proof_roots) AND (` + filter + `) ORDER BY ` + order + ` LIMIT ?`
	params := []any{a.Address, a.Self().Fingerprint(), a.Self().Fingerprint()}
	params = append(params, args...)
	params = append(params, limit)
	rows, err := q.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []historySourceRow
	for rows.Next() {
		var it historySourceRow
		var target, refID, refFP, topicEvent string
		in := &it.in
		if err := rows.Scan(&it.conv, &it.pos.Ms, &in.ID, &it.dir, &in.From, &it.key, &in.TS, &in.Kind, &in.Body, &in.ReplyTo, &in.Status, &in.Sub, &in.Origin, &in.Emotion, &target, &in.PID, &in.LID, &refID, &refFP, &in.AgentID, &in.Quote, &in.Topic, &topicEvent, &in.TopicDone, &it.arrival, &it.outseq); err != nil {
			return nil, err
		}
		if topicEvent != "" {
			if err := json.Unmarshal([]byte(topicEvent), &in.TopicEvent); err != nil {
				return nil, err
			}
		}
		if target != "" {
			if err := json.Unmarshal([]byte(target), &in.Target); err != nil {
				return nil, err
			}
		}
		if refID != "" {
			in.Ref = &envelope.Ref{ID: refID, Fingerprint: refFP}
		}
		in.Conv = it.conv
		it.pos.Conv = it.conv
		it.pos.ID = in.ID
		items = append(items, it)
	}
	return items, rows.Err()
}

func (a *Agent) prepareHistorySource(dev identity.Public, it historySourceRow) (*outCopy, error) {
	_, raw, _, err := a.store.conversation(it.conv)
	if err != nil {
		return nil, err
	}
	if root, err := protocol.ParseConvRoot(raw); err != nil {
		return nil, err
	} else if externalDM(root) {
		me, ok, err := a.store.selfPerson(a.Address)
		if err != nil {
			return nil, err
		}
		if _, member := root.Member(me.info.Person); !ok || !member {
			return nil, nil // advance the snapshot cursor without copying visitor context
		}
	}
	prepared, err := a.historySourceItem(a.store.db, it)
	if err != nil {
		return nil, err
	}
	if root, e := protocol.ParseConvRoot(raw); e == nil && root.Kind == protocol.ConvKindGroup {
		packet, e := groupTurnPacketIn(a.store.db, it.conv)
		if e != nil {
			return nil, e
		}
		me, ok, e := a.store.selfPerson(a.Address)
		if e != nil {
			return nil, e
		}
		member, present := packet.State.Member(me.info.Person)
		var withdrawn int
		if present {
			if e = a.store.db.QueryRow(`SELECT count(*) FROM group_withdrawals WHERE conv=? AND person=? AND admission=?`, it.conv, member.Person, member.Admission.Hash()).Scan(&withdrawn); e != nil {
				return nil, e
			}
		}
		if !ok || !present || withdrawn != 0 || packet.State.Withdrawn(member, packet.Withdrawals) {
			return nil, nil
		}
		if e = groupTurnCheck(a.store.db, packet, a.Address, a.Self().Fingerprint()); e != nil {
			return nil, e
		}
		if e = groupTurnCheck(a.store.db, packet, dev.Address, dev.Fingerprint()); e != nil {
			return nil, e
		}
		// Visitors and previous admissions cannot become an own-history source.
		if !me.has(dev.Address, dev.Fingerprint()) {
			return nil, nil
		}
		if ordinaryGroupTurn(prepared.inner(it.conv)) {
			if retractedRef(a.store.db, it.conv, prepared.LID, prepared.FromKey) {
				return nil, nil
			}
			sources, e := a.groupHistorySources(a.store.db, it.conv, prepared.LID, prepared.FromKey, 0, 64, true)
			if e != nil {
				return nil, e
			}
			found := false
			for _, source := range sources {
				if source.item.ID == prepared.ID {
					// Existing resolver checks every logical duplicate against the
					// visible content hash; never choose a conflicting first row.
					verified, e := a.groupHistorySourceIn(a.store.db, it.conv, historyRef(it.conv, source.item), true)
					if e != nil {
						return nil, e
					}
					prepared = source.item
					prepared.GroupAdmission = verified.stamp
					found = true
					break
				}
			}
			if !found {
				return nil, ErrGroupHistoryUnavailable
			}
			if prepared.GroupAdmission != member.Admission.Hash() && !member.Admission.AllowsHistory(historyRef(it.conv, prepared)) {
				return nil, nil // a known earlier admission is not a new own-live grant
			}
		} else if groupControlSub(prepared.Sub) {
			prepared, e = a.groupControlHistorySource(a.store.db, packet, prepared)
			if errors.Is(e, errGroupControlHistoryEpoch) {
				return nil, nil
			}
			if e != nil {
				return nil, e
			}
		} else if !groupParticipationHistoryItem(prepared) {
			return nil, nil // separately admitted PID traffic has its own history policy
		}
	}
	c, err := a.historyCopy(dev, it.conv, raw, prepared)
	if err != nil {
		if errors.Is(err, errGroupControlHistoryEpoch) || errors.Is(err, errGroupParticipationHistoryEpoch) {
			return nil, nil
		}
		return nil, err
	}

	return &c, nil
}

func (a *Agent) checkHistoryCopies(q dbq, copies []outCopy) error {
	for _, copy := range copies {
		root, e := protocol.ParseConvRoot(copy.in.Root)
		if e != nil {
			return e
		}
		if root.Kind != protocol.ConvKindGroup {
			continue
		}
		packet, e := groupTurnPacketIn(q, copy.in.Conv)
		if e != nil {
			return e
		}
		var item HistoryItem
		if e = decodeStrict([]byte(copy.in.Body), &item); e != nil {
			return e
		}
		own, ok, e := scanPersonIn(q, "state = ?", personSelf)
		if e != nil {
			return e
		}
		if !ok || !own.has(copy.env.To, copy.recipientFP) {
			return ErrGroupContextPending
		}
		admission, e := groupMemberAdmission(q, packet, copy.env.To, copy.recipientFP)
		if e != nil {
			return e
		}
		if admission.Hash() != copy.groupAdmission {
			return ErrGroupContextPending
		}
		if groupControlSub(item.Sub) {
			stamp, e := a.groupControlSourceAdmission(q, packet, item, nil)
			if e != nil {
				return e
			}
			if stamp != item.GroupAdmission {
				return ErrGroupContextPending
			}
			if e = a.groupControlHistoryCheck(q, root, a.Self(), item); e != nil {
				return e
			}
		} else if groupParticipationHistoryItem(item) {
			if e = a.groupParticipationHistoryOutboundCheck(q, packet, copy.env.To, copy.recipientFP, item); e != nil {
				return e
			}
		} else if e = a.groupHistoryOutboundCheck(q, packet, copy.env.To, copy.recipientFP, item); e != nil {
			return e
		}
	}
	return nil
}

// Read only this immutable source and its exact attachment/authority metadata.
// The same reads run inside the queue transaction before publishing a copy.
func (a *Agent) historySourceItem(q dbq, it historySourceRow) (HistoryItem, error) {
	table := "attachments"
	if it.dir == "out" {
		table = "sent_attachments"
	}
	rows, err := q.Query("SELECT name,size,sha256 FROM "+table+" WHERE message_id=? ORDER BY rowid", it.in.ID)
	if err != nil {
		return HistoryItem{}, err
	}
	for rows.Next() {
		var att envelope.Attachment
		if err = rows.Scan(&att.Name, &att.Size, &att.SHA256); err != nil {
			rows.Close()
			return HistoryItem{}, err
		}
		it.in.Attachments = append(it.in.Attachments, att)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return HistoryItem{}, err
	}
	it.in.ReceiverRoute, err = receiverStoredRoute(q, it.dir, it.in.ID)
	if err != nil {
		return HistoryItem{}, err
	}
	it.in.SendGroup, err = storedSendGroup(q, it.dir, it.in.ID)
	if err != nil {
		return HistoryItem{}, err
	}
	it.in.Human, err = storedHuman(q, it.dir, it.in.ID)
	if err != nil {
		return HistoryItem{}, err
	}
	return itemOf(it.in, it.key, it.pos.Ms), nil
}
