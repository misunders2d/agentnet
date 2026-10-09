package client

import (
	"database/sql"
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
	table, messages := "attachments", "inbox"
	if it.dir == "out" {
		table, messages = "sent_attachments", "outbox"
	}
	var topicEvent string
	if err := q.QueryRow("SELECT coalesce(quote,''),coalesce(topic,''),coalesce(topic_event,''),topic_done FROM "+messages+" WHERE id=?", it.in.ID).Scan(&it.in.Quote, &it.in.Topic, &topicEvent, &it.in.TopicDone); err != nil {
		return HistoryItem{}, err
	}
	it.in.TopicEvent = nil
	if topicEvent != "" {
		if err := json.Unmarshal([]byte(topicEvent), &it.in.TopicEvent); err != nil {
			return HistoryItem{}, err
		}
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
	return a.dmLifecycleHistorySource(q, it.conv, itemOf(it.in, it.key, it.pos.Ms))
}

// Public lifecycle disclosure has a member transport sender and a separately
// signed event author. History names that verified author, not the forwarder.
// Group lifecycle history keeps its existing admission/witness policy.
func (a *Agent) dmLifecycleHistorySource(q dbq, conv string, item HistoryItem) (HistoryItem, error) {
	if item.Sub != envelope.SubEvent {
		return item, nil
	}
	root, _, found, err := conversationIn(q, conv)
	if err != nil || !found || root.Kind != protocol.ConvKindDM {
		return item, err
	}
	ev, err := protocol.ParseParticipationEvent([]byte(item.Body))
	if err != nil {
		return item, err
	}
	if ev.Conv != conv || ev.PID != item.PID {
		return item, errors.New("history lifecycle differs from its conversation or participation")
	}
	if ev.Author.Address == item.From && ev.Author.Fingerprint == item.FromKey {
		return item, nil
	}
	m, err := membersIn(q, conv)
	if err != nil {
		return item, err
	}
	if !humanRoom(m) || !m.device(item.From, item.FromKey) {
		return item, errors.New("history lifecycle forwarder is not a current original member")
	}
	transport, pinned, err := pinnedKey(q, item.From)
	if err != nil {
		return item, err
	}
	if item.From == a.Address {
		transport, pinned = a.Self(), true
	}
	if pinned && transport.Fingerprint() != item.FromKey {
		return item, errors.New("history lifecycle forwarder key has changed")
	}
	info, err := participationIn(q, conv, item.PID, m, a.Address)
	if err != nil {
		return item, err
	}
	if err = disclosedCounted(item.inner(conv), info); err != nil {
		return item, err
	}
	key, present, err := pinnedKey(q, ev.Author.Address)
	if err != nil {
		return item, err
	}
	if ev.Author.Address == a.Address {
		key, present = a.Self(), true
	}
	var pending int
	if err = q.QueryRow(`SELECT count(*) FROM peers WHERE address IN (?,?) AND pending IS NOT NULL`, ev.Author.Address, item.From).Scan(&pending); err != nil {
		return item, err
	}
	if !present || pending != 0 || key.Fingerprint() != ev.Author.Fingerprint {
		return item, errors.New("history lifecycle author key is unavailable or changed")
	}
	signed := item.inner(conv)
	signed.From = ev.Author.Address
	if _, err = checkParticipationEvent(signed, ev.Author.Fingerprint, key.SignKey); err != nil {
		return item, err
	}
	// Resolution checked the exact pinned author/host roster and event hash;
	// the original signed bytes, source ID, LID and timestamp remain unchanged.
	item.From, item.FromKey = ev.Author.Address, ev.Author.Fingerprint
	return item, nil
}

// Existing completed jobs can retain a poisoned transport-author copy. Walk
// their ledger once in bounded primary-key pages, scheduling only those exact
// source refs through normal verification. Neither ciphertext nor cursors are
// reset, and a corrected original-author tuple deduplicates subsequent wakes.
func (a *Agent) repairLifecycleHistory(dev identity.Public) (more, seeded bool, err error) {
	key := "history-lifecycle-author-repair/" + dev.Fingerprint()
	var progress struct {
		Pos  [3]string
		Done bool
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback()
	var raw string
	if e := tx.QueryRow(`SELECT v FROM config WHERE k=?`, key).Scan(&raw); e == nil {
		if err = json.Unmarshal([]byte(raw), &progress); err != nil || progress.Done {
			return false, false, err
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return false, false, e
	}
	if err = historyRecoveryCurrent(tx, a.Self(), dev); err != nil {
		return false, false, err
	}
	type ref struct{ conv, author, lid, dir, id string }
	rows, err := tx.Query(`SELECT conv,author,lid,source_dir,source_id FROM history_copies WHERE recipient_fp=? AND (conv,author,lid)>(?,?,?) ORDER BY conv,author,lid LIMIT ?`, dev.Fingerprint(), progress.Pos[0], progress.Pos[1], progress.Pos[2], historyPage)
	if err != nil {
		return false, false, err
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.conv, &r.author, &r.lid, &r.dir, &r.id); err != nil {
			rows.Close()
			return false, false, err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, false, err
	}
	for _, r := range refs {
		progress.Pos = [3]string{r.conv, r.author, r.lid}
		items, e := a.historySourceRows(tx, "dir=? AND id=?", "id", 1, r.dir, r.id)
		if e != nil {
			return false, false, e
		}
		if len(items) != 1 || items[0].conv != r.conv || items[0].key != r.author || items[0].in.LID != r.lid || items[0].in.Sub != envelope.SubEvent {
			continue
		}
		it := items[0]
		ev, e := protocol.ParseParticipationEvent([]byte(it.in.Body))
		if e != nil || ev.Conv != r.conv || ev.PID != it.in.PID || ev.Author.Fingerprint == r.author {
			continue
		}
		root, _, found, e := conversationIn(tx, r.conv)
		if e != nil {
			return false, false, e
		}
		if !found || root.Kind != protocol.ConvKindDM {
			continue
		}
		if corrected, e := a.historySourceItem(tx, it); e == nil && corrected.FromKey != r.author {
			body, _ := json.Marshal(corrected)
			candidate := outCopy{in: envelope.Inner{Conv: r.conv, Body: string(body)}, env: envelope.Envelope{From: a.Address}}
			if present, e := historyCopyPresent(tx, dev, candidate); e != nil {
				return false, false, e
			} else if present {
				if _, err = tx.Exec(`DELETE FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=? AND source_dir=? AND source_id=?`, dev.Fingerprint(), r.conv, r.author, r.lid, r.dir, r.id); err != nil {
					return false, false, err
				}
				continue
			}
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO history_deferred(recipient_fp,dir,id) VALUES(?,?,?)`, dev.Fingerprint(), r.dir, r.id); err != nil {
			return false, false, err
		}
		seeded = true
	}
	progress.Done = len(refs) < historyPage
	data, _ := json.Marshal(progress)
	if _, err = tx.Exec(`INSERT INTO config(k,v) VALUES(?,?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, key, string(data)); err != nil {
		return false, false, err
	}
	return !progress.Done, seeded, tx.Commit()
}
