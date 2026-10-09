package client

import (
	"encoding/json"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A member discloses another inviter's signed public scope to a guest. That
// outgoing transport is not a newly authored event. Copying its history to
// the member's own phone must not claim the transport sender signed the scope.
func TestForwardedParticipationHistoryToOwnPhone(t *testing.T) {
	w, carol, dana, conv, pc, pd, _, stub := twoGuests(t)
	if _, err := carol.AcceptParticipation(tctx(t), pc.PID); err != nil {
		t.Fatal(err)
	}
	if _, err := dana.AcceptParticipation(tctx(t), pd.PID); err != nil {
		t.Fatal(err)
	}
	var shared sharedCopy
	eventually(t, "Alice discloses Bob's signed scope to Carol", func() bool {
		var ok bool
		shared, ok = sharedOf(humanSharedCopies(t, w.alice, conv, pd.PID, carol.Address), protocol.EventScope)
		return ok
	})
	if shared.ev.Author.Address != w.bob.Address || shared.ev.Author.Fingerprint != w.bob.Self().Fingerprint() {
		t.Fatal("fixture did not retain the other original member's event author")
	}
	if err := shared.ev.Verify(w.bob.Self().SignKey); err != nil {
		t.Fatal("fixture forwarded an invalid event signature:", err)
	}
	for _, change := range []struct {
		name string
		edit func(*protocol.ParticipationEvent)
	}{
		{"wrong author", func(e *protocol.ParticipationEvent) { e.Author = stateAuthor(t, carol) }},
		{"wrong conversation", func(e *protocol.ParticipationEvent) { e.Conv = protocol.NewID() + protocol.NewID() }},
		{"wrong participation", func(e *protocol.ParticipationEvent) { e.PID = protocol.NewID() }},
		{"wrong signature", func(e *protocol.ParticipationEvent) { e.Sig = append([]byte(nil), e.Sig...); e.Sig[0] ^= 1 }},
	} {
		t.Run(change.name, func(t *testing.T) {
			e := shared.ev
			change.edit(&e)
			body, _ := json.Marshal(e)
			bad := HistoryItem{V: 1, From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: shared.ev.PID, Body: string(body)}
			if _, err := w.alice.dmLifecycleHistorySource(w.alice.store.db, conv, bad); err == nil {
				t.Fatal("unverified event acquired original-author history attribution")
			}
		})
	}
	t.Run("changed trusted transport pin stays blocked", func(t *testing.T) {
		tx, err := w.bob.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		raw, _ := json.Marshal(shared.ev)
		forwarded := HistoryItem{V: 1, From: w.alice.Address, FromKey: w.alice.Self().Fingerprint(), Kind: envelope.KindMessage, Sub: envelope.SubEvent, PID: shared.ev.PID, Body: string(raw)}
		if _, err := w.bob.dmLifecycleHistorySource(tx, conv, forwarded); err != nil {
			t.Fatal("valid original-member forwarding fixture:", err)
		}
		changed := carol.Self()
		changed.Address = w.alice.Address
		public, _ := json.Marshal(changed)
		if _, err := tx.Exec(`UPDATE peers SET public=?,pending=NULL WHERE address=?`, string(public), w.alice.Address); err != nil {
			t.Fatal(err)
		}
		if _, err := w.bob.dmLifecycleHistorySource(tx, conv, forwarded); err == nil {
			t.Fatal("forwarded lifecycle ignored a changed trusted transport key still present in an older roster")
		}
	})

	phone, awaited, _ := linkPhone(t, w.alice, "forwarded-history-phone")
	request := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), request.ID, true); err != nil {
		t.Fatal(err)
	}
	if result := <-awaited; result.err != nil {
		t.Fatal(result.err)
	}
	// The phone has its real linked identity but no running daemon: deliver
	// only the exact sealed copies below, without background history races.
	rows, err := w.alice.historySourceRows(w.alice.store.db, "dir='out' AND id=?", "id", 1, shared.id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("forwarded lifecycle source: count=%d err=%v", len(rows), err)
	}
	copy, err := w.alice.prepareHistorySource(phone.Self(), rows[0])
	if err != nil || copy == nil {
		t.Fatalf("prepare forwarded lifecycle history: copy=%t err=%v", copy != nil, err)
	}
	var item HistoryItem
	if err := json.Unmarshal([]byte(copy.in.Body), &item); err != nil {
		t.Fatal(err)
	}
	_, root := rootOf(t, w.alice, conv)
	var sharedAccept sharedCopy
	eventually(t, "Alice discloses Dana's acceptance", func() bool {
		var ok bool
		sharedAccept, ok = sharedOf(humanSharedCopies(t, w.alice, conv, pd.PID, carol.Address), protocol.EventAccept)
		return ok
	})
	acceptRows, err := w.alice.historySourceRows(w.alice.store.db, "dir='out' AND id=?", "id", 1, sharedAccept.id)
	if err != nil || len(acceptRows) != 1 {
		t.Fatalf("forwarded acceptance source: count=%d err=%v", len(acceptRows), err)
	}
	oldAccept := itemOf(acceptRows[0].in, acceptRows[0].key, acceptRows[0].pos.Ms)
	acceptCarrier := craft(t, w.alice, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: mustJSON(oldAccept)})
	if err := phone.verifyAndStore(tctx(t), acceptCarrier); err != nil {
		t.Fatal(err)
	}
	historyRecoveryReason(t, phone, acceptCarrier.ID, reasonProof)
	// Older producers put the forwarding member in From even though the
	// enclosed public event keeps its original author's valid signature.
	// A fresh linked phone must recover that exact inert record too.
	legacyItem := item
	legacyItem.From, legacyItem.FromKey = w.alice.Address, w.alice.Self().Fingerprint()
	legacy := craft(t, w.alice, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: mustJSON(legacyItem)})
	if err := phone.verifyAndStore(tctx(t), legacy); err != nil {
		t.Fatal(err)
	}
	if state, err := phone.store.disposition(legacy.ID); err != nil || state != protocol.StateDelivered {
		_, _, checkErr := phone.dmLifecycleHistoryReceived(phone.store.db, legacy.From, conv, legacyItem)
		t.Fatalf("legacy forwarded scope did not recover: state=%s reason=%s check=%v err=%v", state, heldReason(t, phone, legacy.ID), checkErr, err)
	}
	for phone.retryProof(tctx(t)) {
	}
	if state, err := phone.store.disposition(acceptCarrier.ID); err != nil || state != protocol.StateDelivered {
		t.Fatalf("exact acceptance stayed held after its signed scope arrived: %s %v", state, err)
	}
	t.Run("retained invalid copies recover once across restart", func(t *testing.T) {
		var retained []envelope.Envelope
		for range 3 {
			x := craft(t, w.alice, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: mustJSON(legacyItem)})
			if err := phone.store.holdAs(x, reasonInvalid); err != nil {
				t.Fatal(err)
			}
			retained = append(retained, x)
		}
		if err := phone.store.deleteConfig(lifecycleHistoryRecoveryScan); err != nil {
			t.Fatal(err)
		}
		restarted, err := Open(phone.home)
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		for _, x := range retained {
			historyRecoveryReason(t, restarted, x.ID, reasonProof)
		}
		for restarted.retryProof(tctx(t)) {
		}
		for _, x := range retained {
			if got, err := restarted.store.disposition(x.ID); err != nil || got != protocol.StateDelivered {
				t.Fatalf("retained carrier was not admitted and acknowledged: %s %v", got, err)
			}
			if err := restarted.verifyAndStore(tctx(t), x); err != nil {
				t.Fatal(err)
			}
		}
		if got := inboxCount(t, restarted, "lid=? AND claimed_fp=?", item.LID, item.FromKey); got != 1 {
			t.Fatalf("recovery stored %d copies of the same logical event", got)
		}
		if stub.runs() != 0 {
			t.Fatal("history recovery ran an old task")
		}
	})
	for _, badCase := range []string{"wrong transport", "wrong conversation", "wrong participation", "forged signature", "private invitation", "unrelated attachment", "request target", "source ID collision"} {
		t.Run("legacy refuses "+badCase, func(t *testing.T) {
			bad := legacyItem
			bad.ID, bad.LID = protocol.NewID(), protocol.NewID()
			ev := shared.ev
			switch badCase {
			case "wrong transport":
				bad.From, bad.FromKey = carol.Address, carol.Self().Fingerprint()
			case "wrong conversation":
				ev.Conv = protocol.NewID() + protocol.NewID()
			case "wrong participation":
				bad.PID = protocol.NewID()
			case "forged signature":
				ev.Sig = append([]byte(nil), ev.Sig...)
				ev.Sig[0] ^= 1
			case "private invitation":
				ev.Type = protocol.EventInvite
				ev.Sign(w.bob.id.Sign)
			case "unrelated attachment":
				bad.Attachments = []envelope.Attachment{{Name: "unrelated.txt", Size: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
			case "request target":
				bad.Target = &envelope.Target{Address: dana.Address, Fingerprint: dana.Self().Fingerprint()}
			case "source ID collision":
				bad.ID = legacyItem.ID // a different logical source may not overwrite this accepted row
			}
			bad.Body = mustJSON(ev)
			x := craft(t, w.alice, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: mustJSON(bad)})
			if err := phone.verifyAndStore(tctx(t), x); err != nil {
				t.Fatal(err)
			}
			if heldReason(t, phone, x.ID) != reasonInvalid || inboxCount(t, phone, "id=? AND lid=?", bad.ID, bad.LID) != 0 {
				t.Fatal("legacy repair accepted unrelated or unauthenticated history")
			}
		})
	}
	t.Run("authority is rechecked before duplicate acknowledgement", func(t *testing.T) {
		tx, err := phone.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address); err != nil {
			t.Fatal(err)
		}
		if _, _, err := phone.dmLifecycleHistoryReceived(tx, w.alice.Address, conv, legacyItem); err == nil {
			t.Fatal("pending forwarder key bypassed the final transaction guard")
		}
	})

	t.Run("altered signature remains rejected", func(t *testing.T) {
		badEvent := shared.ev
		badEvent.Sig = append([]byte(nil), badEvent.Sig...)
		badEvent.Sig[0] ^= 1
		raw, err := json.Marshal(badEvent)
		if err != nil {
			t.Fatal(err)
		}
		badItem := item
		badItem.ID, badItem.LID = protocol.NewID(), protocol.NewID()
		badItem.From, badItem.FromKey, badItem.Body = w.bob.Address, w.bob.Self().Fingerprint(), string(raw)
		body, err := json.Marshal(badItem)
		if err != nil {
			t.Fatal(err)
		}
		bad := craft(t, w.alice, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Replica: true, Sub: envelope.SubHistory, Body: string(body)})
		if err := phone.verifyAndStore(tctx(t), bad); err != nil {
			t.Fatal(err)
		}
		if heldReason(t, phone, bad.ID) != reasonInvalid || inboxCount(t, phone, "id=?", badItem.ID) != 0 {
			t.Fatal("a forged event signature acquired admission through own-device history")
		}
	})

	if err := phone.verifyAndStore(tctx(t), copy.env); err != nil {
		t.Fatal(err)
	}
	state, err := phone.store.disposition(copy.env.ID)
	if err != nil || state != protocol.StateDelivered {
		_, ownerErr := checkParticipationEvent(item.inner(conv), item.FromKey, w.alice.Self().SignKey)
		t.Fatalf("valid forwarded lifecycle became an invalid own-phone history notice: disposition=%s reason=%s author_is_transport=%t ownership_check=%v err=%v", state, heldReason(t, phone, copy.env.ID), item.From == shared.ev.Author.Address && item.FromKey == shared.ev.Author.Fingerprint, ownerErr, err)
	}
	if stub.runs() != 0 {
		t.Fatal("inert lifecycle history ran a harness")
	}
	t.Run("completed job repairs only old transport tuple", func(t *testing.T) {
		// Use a separate non-running client, as a restarted sender would, so
		// this direct upkeep call does not race the fixture daemon's scheduler.
		source, err := Open(w.alice.home)
		if err != nil {
			t.Fatal(err)
		}
		defer source.Close()
		release, err := lockfile.Wait(source.spoolLockPath())
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if release != nil {
				release()
			}
		}()
		if _, err := source.historyCatchupState(phone.Self()); err != nil {
			t.Fatal(err)
		}
		old, err := source.historyCopy(phone.Self(), conv, root, itemOf(rows[0].in, rows[0].key, rows[0].pos.Ms))
		if err != nil {
			t.Fatal(err)
		}
		old.recipientFP, old.state = phone.Self().Fingerprint(), "quarantined"
		hash, err := historyCopyHash(old)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := source.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := insertCopies(tx, []outCopy{old}); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES(?,?,?,?,?,?,?,?)`, phone.Self().Fingerprint(), conv, rows[0].key, rows[0].in.LID, hash, old.env.ID, "out", shared.id); err != nil {
			t.Fatal(err)
		}
		// Simulate a pre-upgrade job, which could never have indexed this
		// forwarded source under its original author's key.
		if _, err := tx.Exec(`DELETE FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=?`, phone.Self().Fingerprint(), conv, shared.ev.Author.Fingerprint, rows[0].in.LID); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM config WHERE k=?`, "history-lifecycle-author-repair/"+phone.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE history_catchup SET phase='done' WHERE device=?`, phone.Address); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		before, err := source.historyCatchupState(phone.Self())
		if err != nil {
			t.Fatal(err)
		}
		more, seeded, err := source.repairLifecycleHistory(phone.Self())
		if err != nil || more || !seeded {
			t.Fatalf("old tuple was stranded: more=%t seeded=%t err=%v", more, seeded, err)
		}
		var pending, oldCopies int
		if err := source.store.db.QueryRow(`SELECT count(*) FROM history_deferred WHERE recipient_fp=? AND dir='out' AND id=?`, phone.Self().Fingerprint(), shared.id).Scan(&pending); err != nil || pending != 1 {
			t.Fatalf("exact deferred repair: count=%d err=%v", pending, err)
		}
		if err := source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id=? AND state='quarantined'`, old.env.ID).Scan(&oldCopies); err != nil || oldCopies != 1 {
			t.Fatal("repair changed the old retained ciphertext")
		}
		// A new instance/reconnect reads the durable completion marker rather
		// than rescanning or resetting the existing history progress.
		more, seeded, err = source.repairLifecycleHistory(phone.Self())
		after, afterErr := source.historyCatchupState(phone.Self())
		if err != nil || afterErr != nil || more || seeded || before != after {
			t.Fatalf("repair repeated or changed cursors: more=%t seeded=%t err=%v/%v", more, seeded, err, afterErr)
		}
		// A fresh process has no completed in-memory deferred sweep. Normal
		// catch-up must verify and queue the repaired tuple without resetting
		// the completed snapshot cursor or replaying any agent work.
		source.convWork.mu.Lock()
		delete(source.convWork.historyDeferred, phone.Self().Fingerprint())
		source.convWork.mu.Unlock()
		release()
		release = nil
		for range 8 {
			more, err = source.historyCatchupPage(tctx(t), phone.Self())
			if err != nil {
				t.Fatal(err)
			}
			if !more {
				break
			}
		}
		var raw, carrier string
		if err := source.store.db.QueryRow(`SELECT h.carrier,o.envelope FROM history_copies h JOIN outbox o ON o.id=h.carrier WHERE h.recipient_fp=? AND h.conv=? AND h.author=? AND h.lid=?`, phone.Self().Fingerprint(), conv, shared.ev.Author.Fingerprint, rows[0].in.LID).Scan(&carrier, &raw); err != nil {
			t.Fatal("corrected original-author copy was not durably queued:", err)
		}
		var corrected envelope.Envelope
		if err := json.Unmarshal([]byte(raw), &corrected); err != nil {
			t.Fatal(err)
		}
		if err := phone.verifyAndStore(tctx(t), corrected); err != nil {
			t.Fatal(err)
		}
		if got, err := phone.store.disposition(corrected.ID); err != nil || got != protocol.StateDelivered {
			t.Fatalf("repaired ciphertext failed normal receiver admission: %q %v", got, err)
		}
		if err := source.store.setOutboxState(carrier, protocol.StateDelivered, "", ""); err != nil {
			t.Fatal(err)
		}
		var obsolete int
		if err := source.store.db.QueryRow(`SELECT count(*) FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=?`, phone.Self().Fingerprint(), conv, rows[0].key, rows[0].in.LID).Scan(&obsolete); err != nil || obsolete != 0 {
			t.Fatalf("obsolete progress mapping survived verified replacement: %d %v", obsolete, err)
		}
		if err := source.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id=? AND state='quarantined'`, old.env.ID).Scan(&oldCopies); err != nil || oldCopies != 1 {
			t.Fatal("verified replacement removed or misreported the retained quarantine ciphertext")
		}
		jobs, err := source.HistoryProgress()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, job := range jobs {
			if job.Device == phone.Address {
				found = true
				if job.Blocked != 0 || job.Delivered == 0 {
					t.Fatalf("repaired delivery still reports obsolete quarantine: %#v", job)
				}
			}
		}
		if !found {
			t.Fatal("linked device history progress disappeared")
		}
		// Proof can arrive after the one-time repair cursor has finished, by
		// which time another equivalent source may already have queued the
		// corrected copy. Completing that deferred source must retire the old
		// mapping too, without replacing the verified carrier.
		if _, err := source.store.db.Exec(`INSERT INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES(?,?,?,?,?,?,?,?)`, phone.Self().Fingerprint(), conv, rows[0].key, rows[0].in.LID, hash, old.env.ID, "out", shared.id); err != nil {
			t.Fatal(err)
		}
		if _, err := source.store.db.Exec(`INSERT OR IGNORE INTO history_deferred(recipient_fp,dir,id) VALUES(?,'out',?)`, phone.Self().Fingerprint(), shared.id); err != nil {
			t.Fatal(err)
		}
		source.convWork.mu.Lock()
		delete(source.convWork.historyDeferred, phone.Self().Fingerprint())
		source.convWork.mu.Unlock()
		if _, err := source.historyCatchupPage(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
		var again string
		if err := source.store.db.QueryRow(`SELECT carrier FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=?`, phone.Self().Fingerprint(), conv, shared.ev.Author.Fingerprint, rows[0].in.LID).Scan(&again); err != nil || again != carrier {
			t.Fatalf("reconnect replaced verified corrected ciphertext: %q %v", again, err)
		}
		if err := source.store.db.QueryRow(`SELECT count(*) FROM history_copies WHERE recipient_fp=? AND conv=? AND author=? AND lid=?`, phone.Self().Fingerprint(), conv, rows[0].key, rows[0].in.LID).Scan(&obsolete); err != nil || obsolete != 0 {
			t.Fatalf("deferred repair ignored the already verified corrected copy: %d %v", obsolete, err)
		}
		after, err = source.historyCatchupState(phone.Self())
		if err != nil || before.pos != after.pos || stub.runs() != 0 {
			t.Fatal("recovery reset snapshot progress or executed an agent", err)
		}
	})
}
