package client

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

func TestHistoryArchiveImportedTailRemainsBounded(t *testing.T) {
	for _, direct := range []bool{false, true} {
		name := "conversation"
		if direct {
			name = "device"
		}
		t.Run(name, func(t *testing.T) { importedTailRemainsBounded(t, direct) })
	}
}

func importedTailRemainsBounded(t *testing.T, direct bool) {
	const rows = 120
	n := rows
	if direct {
		n = 0
	}
	w, phone, conv, originalIDs := historyCatchupFixture(t, n)
	sub := envelope.SubHistory
	deferredTable := "history_deferred"
	if direct {
		sub = envelope.SubDeviceHistory
		deferredTable = "device_history_pending"
	}
	stopPhone := runAgent(t, phone)
	waitNamedAgentCaps(t, phone)
	label, name, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "phone archive capability", func() bool {
		var profile protocol.Profile
		err := phone.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile)
		return err == nil && profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapHistoryArchive)
	})
	stopPhone()
	a := w.alice
	t.Cleanup(func() { logReplicationFailure(t, a, "laptop"); logReplicationFailure(t, phone, "phone") })
	stop := runAgent(t, a)
	third, awaited, _ := linkPhone(t, a, "third-device")
	req := pendingLink(t, a)
	stop()
	if err := a.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if r := <-awaited; r.err != nil {
		t.Fatal(r.err)
	}
	stopThird := runAgent(t, third)
	waitNamedAgentCaps(t, third)
	stopThird()
	for _, p := range []*backgroundPosts{&a.archivePosting, &a.archiveImporting, &phone.archivePosting, &phone.archiveImporting} {
		p.Lock()
		done := p.done
		if p.cancel != nil {
			p.cancel()
		}
		p.Unlock()
		if done != nil {
			<-done
		}
	}
	a.stopBackgroundPosts()
	phone.stopBackgroundPosts()
	if _, err := a.store.db.Exec(`DELETE FROM outbox WHERE recipient=? AND (state=? OR sub IN (?,'history'))`, phone.Address, archiveStaged, envelope.SubHistoryArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`DELETE FROM history_catchup WHERE device=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`DELETE FROM history_copies WHERE recipient_fp=?`, `DELETE FROM history_deferred WHERE recipient_fp=?`} {
		if _, err := a.store.db.Exec(q, phone.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	if err := phone.store.pin(a.Self()); err != nil {
		t.Fatal(err)
	}
	me, _, err := a.store.selfPerson(a.Address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = phone.refreshPerson(tctx(t), me.info.Person, false); err != nil {
		t.Fatal(err)
	}
	if err = phone.store.pin(third.Self()); err != nil {
		t.Fatal(err)
	}
	if pm, ok, e := phone.store.selfPerson(phone.Address); e == nil && ok {
		var pend int
		phone.store.db.QueryRow(`SELECT count(*) FROM peers WHERE address=? AND pending IS NOT NULL`, third.Address).Scan(&pend)
		_, pinnedFound, _ := pinnedKey(phone.store.db, third.Address)
		t.Logf("phone view: has third=%v human=%v pinned=%v pending=%d; has self=%v", pm.has(third.Address, third.Self().Fingerprint()), pm.roster.Human(third.Self().Fingerprint()), pinnedFound, pend, pm.has(phone.Address, phone.Self().Fingerprint()))
	} else {
		t.Logf("phone selfPerson: ok=%v err=%v", ok, e)
	}
	if err = phone.reconcileHistory(); err != nil {
		t.Fatal(err)
	}
	// The phone starts its catch-up job for the third device first (snapshot).
	pageFor := func(source, target *Agent) error {
		if direct {
			_, e := source.deviceHistoryPage(target.Self())
			return e
		}
		_, e := source.historyCatchupPage(tctx(t), target.Self())
		return e
	}
	if direct {
		for range rows {
			env := sealTo(t, w.bob, a, envelope.Inner{Kind: envelope.KindQuestion, Body: "retained direct original"})
			if err = a.verifyAndStore(tctx(t), env); err != nil {
				t.Fatal(err)
			}
			originalIDs = append(originalIDs, env.ID)
		}
		// These are the laptop's retained bootstrap originals, not new
		// arrivals after its earlier link-created direct-history snapshot.
		if _, err = a.store.db.Exec(`DELETE FROM device_history_jobs WHERE device=? AND fingerprint=?`, phone.Address, phone.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	if err = pageFor(phone, third); err != nil {
		t.Fatalf("phone job for third device: %v", err)
	}
	var before int
	phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=?`, third.Address, sub).Scan(&before)
	// The phone then imports the laptop's bootstrap archives (receipt-driven chunks).
	for page := int64(1); page <= 4; page++ {
		if err = pageFor(a, phone); err != nil {
			t.Fatal(err)
		}
		if _, err = a.archiveStep(tctx(t)); err != nil {
			t.Fatal(err)
		}
		if err = a.FlushOutbox(tctx(t)); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND recipient=? ORDER BY rowid DESC LIMIT 1`, envelope.SubHistoryArchive, phone.Address).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var desc envelope.Envelope
		if err = json.Unmarshal([]byte(raw), &desc); err != nil {
			t.Fatal(err)
		}
		if err = phone.storeReceived(tctx(t), desc); err != nil {
			t.Fatal(err)
		}
		if _, err = phone.archiveImportStep(tctx(t)); err != nil {
			t.Fatal(err)
		}
		if err = a.store.applyReceipt(protocol.ReceiptEvent{ID: desc.ID, State: protocol.StateDelivered, Seq: page}); err != nil {
			t.Fatal(err)
		}
	}
	var imported int
	phone.store.db.QueryRow(`SELECT count(*) FROM history_archive_children`).Scan(&imported)
	for range 5 {
		if err = pageFor(phone, third); err != nil {
			t.Fatal(err)
		}
	}
	var live, staged, total int
	phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=? AND replication_live=1`, third.Address, sub).Scan(&live)
	phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND state='archive_staged'`, third.Address).Scan(&staged)
	phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=?`, third.Address, sub).Scan(&total)
	full, _ := syncWindowFull(phone.store.db, third.Self())
	t.Logf("phone imported %d children from laptop; history copies phone->third: before=%d total=%d live-lane=%d staged=%d windowFull=%v", imported, before, total, live, staged, full)
	if live > 0 {
		t.Errorf("REPRO: %d imported (forwarded) history rows re-sent to another own device as live-lane copies", live)
	}
	if imported != rows || total != historyPage || !full || staged != historyPage {
		t.Fatalf("imported tail escaped bounded archive staging: imported=%d total=%d staged=%d full=%v", imported, total, staged, full)
	}
	var deferred int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM `+deferredTable+` WHERE recipient_fp=?`, third.Self().Fingerprint()).Scan(&deferred); err != nil || deferred != rows-historyPage {
		t.Fatalf("full window lost deferred tail: %d %v", deferred, err)
	}
	_, root := rootOf(t, w.bob, conv)
	fresh := craft(t, w.bob, phone, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: "fresh original after import", Origin: envelope.OriginUI})
	if direct {
		fresh = sealTo(t, w.bob, phone, envelope.Inner{Kind: envelope.KindMessage, Body: "fresh direct original after import"})
	}
	if err = phone.verifyAndStore(tctx(t), fresh); err != nil {
		t.Fatal(err)
	}
	if err = pageFor(phone, third); err != nil {
		t.Fatal(err)
	}
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=? AND replication_live=1`, third.Address, sub).Scan(&live); err != nil || live != 1 {
		t.Fatalf("fresh original did not bypass cold window: %d %v", live, err)
	}
	if err = third.store.pin(phone.Self()); err != nil {
		t.Fatal(err)
	}
	for page := int64(1); page <= 3; page++ {
		for range 3 {
			if _, err = phone.archiveStep(tctx(t)); err != nil {
				t.Fatal(err)
			}
		}
		if err = phone.FlushOutbox(tctx(t)); err != nil {
			t.Fatal(err)
		}
		var raw string
		if err = phone.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=? AND sub=? ORDER BY rowid DESC LIMIT 1`, third.Address, envelope.SubHistoryArchive).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var desc envelope.Envelope
		if err = json.Unmarshal([]byte(raw), &desc); err != nil {
			t.Fatal(err)
		}
		if err = third.storeReceived(tctx(t), desc); err != nil {
			t.Fatal(err)
		}
		if _, err = third.archiveImportStep(tctx(t)); err != nil {
			t.Fatal(err)
		}
		state, e := third.store.disposition(desc.ID)
		if e != nil || state != protocol.StateDelivered {
			t.Fatalf("third device did not retain archive: %s %v", state, e)
		}
		if err = phone.store.applyReceipt(protocol.ReceiptEvent{ID: desc.ID, State: state, Seq: page}); err != nil {
			t.Fatal(err)
		}
		if err = pageFor(phone, third); err != nil {
			t.Fatal(err)
		}
	}
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM `+deferredTable+` WHERE recipient_fp=?`, third.Self().Fingerprint()).Scan(&deferred); err != nil || deferred != 0 {
		t.Fatalf("receipt-driven pages did not drain deferred tail: %d %v", deferred, err)
	}
	var retained int
	if err = phone.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND sub=? AND state=?`, third.Address, sub, archiveAccepted).Scan(&retained); err != nil || retained != rows {
		t.Fatalf("not all imported originals retained on third device: %d %v", retained, err)
	}
	for _, id := range originalIDs {
		var replica int
		if err = third.store.db.QueryRow(`SELECT replica FROM inbox WHERE id=?`, id).Scan(&replica); err != nil || replica != 1 {
			t.Fatalf("third device lacks exact inert original %s: replica=%d %v", id, replica, err)
		}
	}
}
