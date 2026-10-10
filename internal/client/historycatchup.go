package client

import (
	"bytes"
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
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Appended independently of the original snapshot position. Existing completed
// jobs get one bounded accepted-row reconciliation; future late history follows
// inbox.arrival, never the old message's timestamp or conversation sort order.
const historyCatchupSchema = `
CREATE TABLE history_catchup(
 device TEXT NOT NULL, fingerprint TEXT NOT NULL, phase TEXT NOT NULL,
 pos TEXT NOT NULL, inbox_ceiling INTEGER NOT NULL, outbox_ceiling INTEGER NOT NULL,
 tail INTEGER NOT NULL, context_pos TEXT NOT NULL DEFAULT '', context_done INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(device,fingerprint));
CREATE TABLE history_copies(
 recipient_fp TEXT NOT NULL,conv TEXT NOT NULL,author TEXT NOT NULL,lid TEXT NOT NULL,
 hash TEXT NOT NULL,carrier TEXT NOT NULL,source_dir TEXT NOT NULL,source_id TEXT NOT NULL,
 PRIMARY KEY(recipient_fp,conv,author,lid));
CREATE TABLE history_deferred(
 recipient_fp TEXT NOT NULL,dir TEXT NOT NULL,id TEXT NOT NULL,
 PRIMARY KEY(recipient_fp,dir,id));
`

type historyCatchupProgress struct {
	context             string
	contextDone         bool
	phase               string
	pos                 historyPos
	inbox, outbox, tail int64
}

type historyDeferredScan struct {
	dir, id string
	done    bool
}

var errHistoryCatchupConflict = errors.New("history logical source differs from its queued copy")

func (a *Agent) historyCatchupState(dev identity.Public) (historyCatchupProgress, error) {
	var p historyCatchupProgress
	var raw string
	err := a.store.db.QueryRow(`SELECT phase,pos,inbox_ceiling,outbox_ceiling,tail,context_pos,context_done FROM history_catchup WHERE device=? AND fingerprint=?`, dev.Address, dev.Fingerprint()).Scan(&p.phase, &raw, &p.inbox, &p.outbox, &p.tail, &p.context, &p.contextDone)
	if errors.Is(err, sql.ErrNoRows) {
		tx, e := a.store.db.Begin()
		if e != nil {
			return p, e
		}
		defer tx.Rollback()
		if e = historyRecoveryCurrent(tx, a.Self(), dev); e != nil {
			return p, e
		}
		if e = tx.QueryRow(`SELECT CAST(v AS INTEGER) FROM config WHERE k='arrival'`).Scan(&p.inbox); e != nil {
			return p, e
		}
		if e = tx.QueryRow(`SELECT coalesce(max(rowid),0) FROM outbox`).Scan(&p.outbox); e != nil {
			return p, e
		}
		p.phase, p.tail = "recent", p.inbox
		data, _ := json.Marshal(p.pos)
		if _, e = tx.Exec(`INSERT INTO history_catchup(device,fingerprint,phase,pos,inbox_ceiling,outbox_ceiling,tail) VALUES(?,?,?,?,?,?,?)`, dev.Address, dev.Fingerprint(), p.phase, string(data), p.inbox, p.outbox, p.tail); e != nil {
			return p, e
		}
		// Once a job enters this narrower path, it must not fall back to legacy
		// agent authority if either device later loses its human role.
		guard, _ := json.Marshal(historyRecoveryDevices{Sender: a.Self(), Reader: dev})
		if _, e = tx.Exec(`INSERT OR IGNORE INTO config(k,v) VALUES(?,?)`, discoveredHistory+dev.Address, string(guard)); e != nil {
			return p, e
		}
		return p, a.store.done(tx.Commit())
	}
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal([]byte(raw), &p.pos); err != nil {
		return p, err
	}
	return p, historyRecoveryCurrent(a.store.db, a.Self(), dev)
}

// The signed original content and exact control target bind a logical copy.
// Recipient ciphertext, receive time and historical group witnesses do not.
func historyCopyHash(c outCopy) (string, error) {
	var item HistoryItem
	if err := decodeStrict([]byte(c.in.Body), &item); err != nil {
		return "", err
	}
	data, _ := json.Marshal(struct {
		Content string
		Ref     *envelope.Ref
	}{historyRef(c.in.Conv, item).Hash, item.Ref})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func historyCopyPresent(q dbq, dev identity.Public, c outCopy) (bool, error) {
	var item HistoryItem
	if err := decodeStrict([]byte(c.in.Body), &item); err != nil {
		return false, err
	}
	hash, err := historyCopyHash(c)
	if err != nil {
		return false, err
	}
	var prior, state, fp, conv, sub, raw, body, carrier string
	err = q.QueryRow(`SELECT h.hash,h.carrier,coalesce(o.state,''),coalesce(o.recipient_fp,''),coalesce(o.conv,''),coalesce(o.sub,''),coalesce(o.envelope,''),coalesce(o.body,'') FROM history_copies h LEFT JOIN outbox o ON o.id=h.carrier AND o.recipient=? WHERE h.recipient_fp=? AND h.conv=? AND h.author=? AND h.lid=?`, dev.Address, dev.Fingerprint(), c.in.Conv, item.FromKey, item.LID).Scan(&prior, &carrier, &state, &fp, &conv, &sub, &raw, &body)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != prior {
		return false, fmt.Errorf("%w: retained ledger", errHistoryCatchupConflict)
	}
	if fp != dev.Fingerprint() || conv != c.in.Conv || sub != envelope.SubHistory {
		return false, nil
	}
	var env envelope.Envelope
	if json.Unmarshal([]byte(raw), &env) != nil || env.ID != carrier || env.From != c.env.From || env.To != dev.Address || env.V != envelope.Version2 || env.Kind != envelope.KindMessage {
		return false, nil
	}
	if body != "" {
		held := c
		held.in.Body = body
		var old HistoryItem
		if decodeStrict([]byte(body), &old) != nil || old.FromKey != item.FromKey || old.LID != item.LID {
			return false, nil
		}
		bound, e := historyCopyHash(held)
		if e != nil || bound != hash {
			return false, nil
		}
	}
	// Quarantined means the receiver retained this exact ciphertext. Its
	// normal proof recovery owns reconsideration; a new ID only floods it.
	return state == stateQueued || state == "waiting" || state == "custody" || state == "delivered" || state == "quarantined", nil
}

func (a *Agent) historyDependencies(it historySourceRow, prepared HistoryItem) ([]historySourceRow, error) {
	var deps []historySourceRow
	if it.in.Sub == envelope.SubEvent {
		ev, e := protocol.ParseParticipationEvent([]byte(it.in.Body))
		if e != nil {
			return nil, e
		}
		if ev.Prev != "" {
			found := false
			// historyCopy already reverified this exact source's witness. Its
			// signed predecessor travels with the copy and stays inert; it does
			// not need a separately installed inbox or live participation row.
			if prepared.ID == it.in.ID && prepared.PID == it.in.PID && prepared.GroupHistory != nil {
				for _, candidate := range prepared.GroupHistory.Memberships {
					if candidate.Conv == it.conv && candidate.PID == it.in.PID && candidate.Hash() == ev.Prev {
						found = true
						break
					}
				}
			}
			if !found {
				rows, e := a.historySourceRows(a.store.db, "conv=? AND pid=? AND sub='event'", "ms,id", maxPendingPerConversation+1, it.conv, it.in.PID)
				if e != nil {
					return nil, e
				}
				if len(rows) > maxPendingPerConversation {
					return nil, errTooManyEvents
				}
				for _, row := range rows {
					candidate, e := protocol.ParseParticipationEvent([]byte(row.in.Body))
					if e == nil && candidate.Hash() == ev.Prev {
						deps = append(deps, row)
						found = true
					}
				}
			}
			if !found {
				return nil, ErrGroupContextPending
			}
		}
	}
	if it.in.PID != "" && it.in.Sub != envelope.SubEvent {
		rows, err := a.historySourceRows(a.store.db, "conv=? AND pid=? AND sub='event'", "ms,id", maxPendingPerConversation+1, it.conv, it.in.PID)
		if err != nil {
			return nil, err
		}
		if len(rows) > maxPendingPerConversation {
			return nil, errTooManyEvents
		}
		deps = append(deps, rows...)
	}
	ref, key := "", ""
	if it.in.Ref != nil {
		ref, key = it.in.Ref.ID, it.in.Ref.Fingerprint
	} else if it.in.ReplyTo != "" {
		ref = it.in.ReplyTo
	}
	if ref != "" {
		filter := "conv=? AND (id=? OR lid=?)"
		args := []any{it.conv, ref, ref}
		if key != "" {
			filter += " AND coalesce(verified_by,claimed_fp,'')=?"
			args = append(args, key)
		}
		rows, err := a.historySourceRows(a.store.db, filter, "ms,id", 2, args...)
		if err != nil {
			return nil, err
		}
		if len(rows) == 0 && it.in.Ref != nil {
			return nil, ErrGroupContextPending
		}
		// Reply ancestry is ordering, not admission. The independently verified
		// copy can arrive without an older parent absent from this device, as in
		// browser catch-up. Exact control refs and signed event proofs still wait.
		deps = append(deps, rows...)
	}
	return deps, nil
}

// One source page, one independent arrival tail page, and one deferred page.
// Deferred refs are swept once per existing external proof/reconnect wake;
// their mere presence never causes immediate retries of unchanged evidence.
func (a *Agent) historyCatchupPage(ctx context.Context, dev identity.Public) (more bool, err error) {
	if err = historyCatchupAuthority(a.store.db, a.Self(), dev); err != nil {
		return false, err
	}
	release, err := lockfile.Wait(a.spoolLockPath())
	if err != nil {
		return false, err
	}
	defer release()
	p, err := a.historyCatchupState(dev)
	if err != nil {
		return false, err
	}
	repairMore, repairSeeded, err := a.repairLifecycleHistory(dev)
	if err != nil {
		return false, err
	}
	defer func() { more = more || err == nil && repairMore }()
	before := p
	a.convWork.mu.Lock()
	if a.convWork.historyDeferred == nil {
		a.convWork.historyDeferred = map[string]historyDeferredScan{}
	}
	sweep, knownWake := a.convWork.historyDeferred[dev.Fingerprint()]
	a.convWork.mu.Unlock()
	if repairSeeded {
		sweep, knownWake = historyDeferredScan{}, false
	}

	if !knownWake {
		// Failed/expired copies remain eligible on the next existing wake.
		// This schedules exact source refs only; their authority is checked
		// again before any replacement ciphertext is committed.
		if _, e := a.store.db.Exec(`INSERT OR IGNORE INTO history_deferred(recipient_fp,dir,id) SELECT h.recipient_fp,h.source_dir,h.source_id FROM history_copies h LEFT JOIN outbox o ON o.id=h.carrier WHERE h.recipient_fp=? AND coalesce(o.state,'') NOT IN ('queued','waiting','custody','delivered','quarantined')`, dev.Fingerprint()); e != nil {
			return false, e
		}
		p.context = ""
		p.contextDone = false
	}
	var items []historySourceRow
	var contextConvs []string
	if !p.contextDone {
		rows, e := a.store.db.Query(`SELECT conv FROM (SELECT conv FROM group_context UNION SELECT conv FROM group_proof_roots) WHERE conv>? ORDER BY conv LIMIT ?`, p.context, historyPage)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var conv string
			if e = rows.Scan(&conv); e != nil {
				rows.Close()
				return false, e
			}
			contextConvs = append(contextConvs, conv)
			p.context = conv
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		p.contextDone = len(contextConvs) < historyPage
		more = more || !p.contextDone
	}
	var tailCeiling int64
	if err = a.store.db.QueryRow(`SELECT CAST(v AS INTEGER) FROM config WHERE k='arrival'`).Scan(&tailCeiling); err != nil {
		return false, err
	}
	tail, err := a.historySourceRows(a.store.db, "dir='in' AND arrival>? AND arrival<=?", "arrival", historyPage, p.tail, tailCeiling)
	if err != nil {
		return false, err
	}
	items = append(items, tail...)
	if len(tail) == historyPage {
		p.tail = tail[len(tail)-1].arrival
		more = true
	} else {
		p.tail = tailCeiling
	}
	// Visit one bounded newest page per conversation using its existing
	// timestamp index, never repeatedly rank the whole message corpus.
	if p.phase == "recent" {
		for conversations := 0; conversations < historyPage; conversations++ {
			latest, e := a.historySourceRows(a.store.db, "conv>? AND arrival<=? AND outseq<=?", "conv,ms DESC,id DESC", 1, p.pos.Conv, p.inbox, p.outbox)
			if e != nil {
				return false, e
			}
			if len(latest) == 0 {
				p.phase = "older"
				p.pos = historyPos{}
				break
			}
			page, e := a.historySourceRows(a.store.db, "conv=? AND arrival<=? AND outseq<=?", "ms DESC,id DESC", historyPage, latest[0].conv, p.inbox, p.outbox)
			if e != nil {
				return false, e
			}
			if len(items) > len(tail) && len(items)-len(tail)+len(page) > historyPage {
				more = true
				break
			}
			items = append(items, page...)
			p.pos = historyPos{Conv: latest[0].conv}
			// At most one full page, or up to 50 sparse conversations, per pass.
			if len(items)-len(tail) >= historyPage {
				more = true
				break
			}
			if conversations == historyPage-1 {
				more = true
			}
		}
	}
	if p.phase == "older" {
		page, e := a.historySourceRows(a.store.db, "arrival<=? AND outseq<=? AND (conv,ms,id)>(?,?,?)", "conv,ms,id", historyPage, p.inbox, p.outbox, p.pos.Conv, p.pos.Ms, p.pos.ID)
		if e != nil {
			return false, e
		}
		items = append(items, page...)
		if len(page) > 0 {
			p.pos = page[len(page)-1].pos
		}
		if len(page) == historyPage {
			more = true
		} else {
			p.phase = "done"
		}
	}
	if !sweep.done {
		rows, e := a.store.db.Query(`SELECT dir,id FROM history_deferred WHERE recipient_fp=? AND (dir,id)>(?,?) ORDER BY dir,id LIMIT ?`, dev.Fingerprint(), sweep.dir, sweep.id, historyPage)
		if e != nil {
			return false, e
		}
		var refs []historyDeferredScan
		for rows.Next() {
			var r historyDeferredScan
			if e = rows.Scan(&r.dir, &r.id); e != nil {
				rows.Close()
				return false, e
			}
			refs = append(refs, r)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return false, e
		}
		for _, ref := range refs {
			if ref.dir == "context" {
				contextConvs = append(contextConvs, ref.id)
				sweep = ref
				continue
			}
			source, e := a.historySourceRows(a.store.db, "dir=? AND id=?", "conv,ms,id", 1, ref.dir, ref.id)
			if e != nil {
				return false, e
			}
			items = append(items, source...)
			sweep = ref
		}
		sweep.done = len(refs) < historyPage
		more = more || !sweep.done
	}
	var copies []outCopy
	var sources []historySourceRow
	sourceByCopy := map[string]historySourceRow{}
	var deferred []historySourceRow
	batches := map[string][]groupHistoryBatch{}
	var prepared []outCopy
	defer func() { a.releaseGroupCopies(prepared) }()
	var deferredContexts []string
	for _, conv := range contextConvs {
		if _, ok := batches[conv]; ok {
			continue
		}
		batch, e := a.prepareGroupHistoryCarriers(ctx, dev, conv)
		if e != nil {
			deferredContexts = append(deferredContexts, conv)
			continue
		}
		batches[conv] = batch
		for _, b := range batch {
			prepared = append(prepared, b.copies...)
		}
	}
	done := map[string]bool{}
	logical := map[string]string{}
	visiting := map[string]bool{}
	var queue func(historySourceRow) error
	queue = func(it historySourceRow) error {
		id := it.dir + "/" + it.in.ID
		if done[id] {
			return nil
		}
		if it.in.From == dev.Address && it.key == dev.Fingerprint() {
			done[id] = true
			return nil
		}
		if via, e := historyVia(a.store.db, it); e != nil {
			return e
		} else if via == dev.Address {
			done[id] = true // it came here from that device, which holds it
			return nil
		}
		if visiting[id] {
			return ErrGroupContextPending
		}
		visiting[id] = true
		defer delete(visiting, id)
		c, e := a.prepareHistorySource(dev, it)
		if e != nil {
			return e
		}
		if c == nil {
			// The shared source policy deliberately excludes this row (for
			// example a retraction or previous admission). Missing evidence is
			// an error above; an intentional skip must not become a stuck job.
			done[id] = true
			return nil
		}
		c.recipientFP = dev.Fingerprint()
		if _, ok := batches[it.conv]; !ok {
			batch, e := a.prepareGroupHistoryCarriers(ctx, dev, it.conv)
			if e != nil {
				return e
			}
			batches[it.conv] = batch
			for _, b := range batch {
				prepared = append(prepared, b.copies...)
			}
		}
		var item HistoryItem
		if e = decodeStrict([]byte(c.in.Body), &item); e != nil {
			return e
		}
		logicalID := it.conv + "/" + item.FromKey + "/" + item.LID
		hash, e := historyCopyHash(*c)
		if e != nil {
			return e
		}
		if prior, ok := logical[logicalID]; ok {
			if prior != hash {
				return fmt.Errorf("%w: page logical duplicate", errHistoryCatchupConflict)
			}
			if it.key != item.FromKey {
				copies = append(copies, *c)
				sources = append(sources, it)
				sourceByCopy[c.env.ID] = it
			}
			done[id] = true
			return nil
		}
		present, e := historyCopyPresent(a.store.db, dev, *c)
		if e != nil {
			return e
		}
		if present {
			if it.key != item.FromKey {
				// Revalidate this deferred source in the final transaction even
				// when its corrected copy arrived through an equivalent source.
				copies = append(copies, *c)
				sources = append(sources, it)
				sourceByCopy[c.env.ID] = it
			}
			done[id] = true
			return nil
		}
		if len(visiting) > 64 || len(copies) >= 4*historyPage {
			return ErrGroupContextPending
		}
		deps, e := a.historyDependencies(it, item)
		if e != nil {
			return e
		}
		for _, dep := range deps {
			if dep.dir == it.dir && dep.in.ID == it.in.ID {
				continue
			}
			if e = queue(dep); e != nil {
				return e
			}
		}
		if !present {
			copies = append(copies, *c)
			sources = append(sources, it)
			sourceByCopy[c.env.ID] = it
		}
		logical[logicalID] = hash
		done[id] = true
		return nil
	}
	for _, it := range items {
		if e := queue(it); e != nil {
			if errors.Is(e, errHistoryCatchupConflict) {
				return false, e
			}
			deferred = append(deferred, it)
		}
	}
	if len(copies) == 0 && len(prepared) == 0 && len(deferred) == 0 && len(deferredContexts) == 0 && before == p && sweep.done {
		a.convWork.mu.Lock()
		a.convWork.historyDeferred[dev.Fingerprint()] = sweep
		a.convWork.mu.Unlock()
		return more, nil
	}
	tx, err := a.store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = historyCatchupAuthority(tx, a.Self(), dev); err != nil {
		return false, err
	}
	if err = a.discoveredHistoryCheck(tx, dev.Address); err != nil {
		return false, err
	}
	// Sources and progress share the transaction with each encrypted copy. A
	// deletion or replacement during preparation cancels this whole page.
	for i, it := range sources {
		current, e := a.historySourceRows(tx, "dir=? AND id=?", "conv,ms,id", 1, it.dir, it.in.ID)
		if e != nil {
			return false, e
		}
		if len(current) != 1 {
			return false, ErrGroupHistoryUnavailable
		}
		raw, _ := json.Marshal(it.in)
		fresh, _ := json.Marshal(current[0].in)
		if it.key != current[0].key || !bytes.Equal(raw, fresh) {
			return false, fmt.Errorf("%w: transaction source row", errHistoryCatchupConflict)
		}
		item, e := a.historySourceItem(tx, current[0])
		if e != nil {
			return false, e
		}
		var offered HistoryItem
		if e = decodeStrict([]byte(copies[i].in.Body), &offered); e != nil {
			return false, e
		}
		if historyRef(it.conv, item).Hash != historyRef(it.conv, offered).Hash || !sameHistoryRef(item.Ref, offered.Ref) {
			return false, fmt.Errorf("%w: transaction source metadata", errHistoryCatchupConflict)
		}
	}
	var carriers []outCopy
	for _, batch := range batches {
		for _, b := range batch {
			if err = a.checkGroupHistoryBatch(tx, dev, b); err != nil {
				return false, err
			}
			complete, e := a.groupHistoryBatchPresent(tx, dev, b.packet, b.payloads)
			if e != nil {
				return false, e
			}
			if !complete {
				if len(b.copies) == 0 {
					return false, ErrGroupContextPending
				}
				carriers = append(carriers, b.copies...)
			}
		}
	}
	if err = a.checkHistoryCopies(tx, copies); err != nil {
		return false, err
	}
	var freshCopies []outCopy
	for _, c := range copies {
		present, e := historyCopyPresent(tx, dev, c)
		if e != nil {
			return false, e
		}
		var item HistoryItem
		if e = decodeStrict([]byte(c.in.Body), &item); e != nil {
			return false, e
		}
		if !present {
			hash, e := historyCopyHash(c)
			if e != nil {
				return false, e
			}
			if _, e = tx.Exec(`INSERT INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(recipient_fp,conv,author,lid) DO UPDATE SET carrier=excluded.carrier`, dev.Fingerprint(), c.in.Conv, item.FromKey, item.LID, hash, c.env.ID, sourceByCopy[c.env.ID].dir, sourceByCopy[c.env.ID].in.ID); e != nil {
				return false, e
			}
			freshCopies = append(freshCopies, c)
		}
		if source := sourceByCopy[c.env.ID]; source.key != item.FromKey {
			// Retire only the obsolete transport attribution, atomically with
			// its verified original-author copy. Retained ciphertext stays put.
			if _, e = tx.Exec(`DELETE FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=? AND source_dir=? AND source_id=?`, dev.Fingerprint(), c.in.Conv, source.key, item.LID, source.dir, source.in.ID); e != nil {
				return false, e
			}
		}
	}
	if err = insertCopies(tx, append(carriers, freshCopies...)); err != nil {
		return false, err
	}
	for _, c := range carriers {
		for _, att := range c.in.Attachments {
			if _, err = tx.Exec(`INSERT INTO sent_attachments(message_id,blob_id,name,size,sha256) VALUES(?,?,?,?,?)`, c.env.ID, att.Blob.ID, att.Name, att.Size, att.SHA256); err != nil {
				return false, err
			}
		}
		for _, blob := range c.env.Blobs {
			if _, err = tx.Exec(`INSERT INTO uploads(blob_id,message_id,state) VALUES(?,?,?)`, blob.ID, c.env.ID, protocol.BlobUploading); err != nil {
				return false, err
			}
		}
	}
	for _, it := range items {
		if done[it.dir+"/"+it.in.ID] {
			if _, err = tx.Exec(`DELETE FROM history_deferred WHERE recipient_fp=? AND dir=? AND id=?`, dev.Fingerprint(), it.dir, it.in.ID); err != nil {
				return false, err
			}
		}
	}
	for _, it := range deferred {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO history_deferred(recipient_fp,dir,id) VALUES(?,?,?)`, dev.Fingerprint(), it.dir, it.in.ID); err != nil {
			return false, err
		}
	}
	for conv := range batches {
		if _, err = tx.Exec(`DELETE FROM history_deferred WHERE recipient_fp=? AND dir='context' AND id=?`, dev.Fingerprint(), conv); err != nil {
			return false, err
		}
	}
	for _, conv := range deferredContexts {
		if _, err = tx.Exec(`INSERT OR IGNORE INTO history_deferred(recipient_fp,dir,id) VALUES(?,'context',?)`, dev.Fingerprint(), conv); err != nil {
			return false, err
		}
	}
	data, _ := json.Marshal(p.pos)
	old, _ := json.Marshal(before.pos)
	res, err := tx.Exec(`UPDATE history_catchup SET phase=?,pos=?,tail=?,context_pos=?,context_done=? WHERE device=? AND fingerprint=? AND phase=? AND pos=? AND tail=? AND context_pos=? AND context_done=?`, p.phase, string(data), p.tail, p.context, p.contextDone, dev.Address, dev.Fingerprint(), before.phase, string(old), before.tail, before.context, before.contextDone)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n != 1 {
		return false, fmt.Errorf("history catch-up position changed")
	}
	if p.phase == "done" {
		if _, err = tx.Exec(`UPDATE history_jobs SET state='done',updated_at=? WHERE device=? AND fingerprint=? AND state='running'`, time.Now().Unix(), dev.Address, dev.Fingerprint()); err != nil {
			return false, err
		}
	}
	if err = a.store.done(tx.Commit()); err != nil {
		return false, err
	}
	a.convWork.mu.Lock()
	a.convWork.historyDeferred[dev.Fingerprint()] = sweep
	a.convWork.mu.Unlock()
	committed := map[string]bool{}
	for _, c := range carriers {
		committed[c.in.ID] = true
	}
	kept := prepared[:0]
	for _, c := range prepared {
		if !committed[c.in.ID] {
			kept = append(kept, c)
		}
	}
	prepared = kept
	if len(carriers)+len(freshCopies) > 0 {
		a.kickNow()
	}
	return more, nil
}

func sameHistoryRef(a, b *envelope.Ref) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func historyCatchupAuthority(q dbq, self, dev identity.Public) error {
	if err := historyRecoveryCurrent(q, self, dev); err != nil {
		return err
	}
	var n int
	if err := q.QueryRow(`SELECT count(*) FROM history_jobs WHERE device=? AND fingerprint=? AND state IN ('running','done')`, dev.Address, dev.Fingerprint()).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return errHistoryRecoveryAuthority
	}
	return nil
}
