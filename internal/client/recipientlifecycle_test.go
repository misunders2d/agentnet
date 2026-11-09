package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestRecipientLifecyclePermanentRemovalPreservesEvidence(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	states := []string{stateQueued, stateConvWaiting, stateReceiverWaiting, archiveStaged, archiveUploading, protocol.StateCustody, protocol.StateDelivered, archiveAccepted}
	for i, state := range states {
		if _, err = s.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,sub,body,envelope,state,created_at) VALUES(?,'person/phone','old-key','history','original','exact-ciphertext',?,1)`, fmt.Sprint(i), state); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`INSERT INTO history_jobs VALUES('person/phone','old-key','{}',1,'running',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err = s.endRecipient("person/phone", "replacement-key", true); err != nil {
		t.Fatal(err)
	}
	var n int
	s.db.QueryRow(`SELECT count(*) FROM outbox WHERE state='not_delivered'`).Scan(&n)
	if n != 0 {
		t.Fatal("replacement key ended old copies")
	}
	for twice := 0; twice < 2; twice++ {
		if err = s.endRecipient("person/phone", "old-key", true); err != nil {
			t.Fatal(err)
		}
	}
	for i, state := range states {
		var got, body, wire string
		if err = s.db.QueryRow(`SELECT state,body,envelope FROM outbox WHERE id=?`, fmt.Sprint(i)).Scan(&got, &body, &wire); err != nil {
			t.Fatal(err)
		}
		want := state
		if i < 5 {
			want = stateNotDelivered
		}
		if got != want || body != "original" || wire != "exact-ciphertext" {
			t.Fatalf("%s: %s, retained=%v", state, got, body == "original" && wire == "exact-ciphertext")
		}
	}
	var job string
	if err = s.db.QueryRow(`SELECT state FROM history_jobs`).Scan(&job); err != nil || job != "ended" {
		t.Fatalf("job=%s err=%v", job, err)
	}
	if _, err = s.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES('dm','person/standalone','key','original','exact', 'queued',1)`); err != nil {
		t.Fatal(err)
	}
	if err = s.endRecipient("person/standalone", "key", false); err != nil {
		t.Fatal(err)
	}
	var dm string
	s.db.QueryRow(`SELECT state FROM outbox WHERE id='dm'`).Scan(&dm)
	if dm != stateQueued {
		t.Fatalf("detached standalone DM=%s", dm)
	}
	old, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public := old.Public("person/standalone")
	fp := public.Fingerprint()
	raw, err := json.Marshal(protocol.PersonRoster{Person: "owner", Label: "owner", Devices: []identity.Public{public}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO person_chain(person,seq,hash,record) VALUES('owner',0,'hash',?)`, string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO persons(person,label,seq,hash,record,state,pinned_at) VALUES('owner','owner',0,'hash','{}',?,1)`, personSelf); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,sub,body,envelope,state,created_at) VALUES('sync','person/standalone',?,'device-history','original','exact','archive_staged',1)`, fp); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,sub,body,envelope,state,created_at) VALUES('foreign-history','foreign/device',?,'history','selected','exact','queued',1)`, fp); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO history_jobs VALUES('foreign/device',?,'{}',1,'running',1,1)`, fp); err != nil {
		t.Fatal(err)
	}
	if err = s.endRecipient("foreign/device", fp, false); err != nil {
		t.Fatal(err)
	}
	var foreign string
	if err = s.db.QueryRow(`SELECT state FROM outbox WHERE id='foreign-history'`).Scan(&foreign); err != nil || foreign != stateQueued {
		t.Fatalf("foreign selected history=%s err=%v", foreign, err)
	}
	// A roster refreshed between discovery and the transaction keeps copies.
	if _, err = s.db.Exec(`INSERT INTO person_devices VALUES('person/standalone','owner',?,1)`, fp); err != nil {
		t.Fatal(err)
	}
	if err = s.endRecipient("person/standalone", fp, false); err != nil {
		t.Fatal(err)
	}
	var syncState string
	s.db.QueryRow(`SELECT state FROM outbox WHERE id='sync'`).Scan(&syncState)
	if syncState != archiveStaged {
		t.Fatalf("current roster sync=%s", syncState)
	}
	// A still-enrolled former own phone may receive a new independently granted
	// selected share. A same-target own carrier is identified by its existing ledger.
	if _, err = s.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,sub,body,envelope,state,created_at) VALUES('former-selected','person/standalone',?,'history','selected','exact','queued',1),('own-carrier','person/standalone',?,'history','own history','exact','queued',1),('own-prepack','person/standalone',?,'history','kept history','exact-prepack','archive_staged',1)`, fp, fp, fp); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO history_copies(recipient_fp,conv,author,lid,hash,carrier,source_dir,source_id) VALUES(?,'conv','author','lid','hash','own-carrier','in','original')`, fp); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM person_devices WHERE address='person/standalone'`); err != nil {
		t.Fatal(err)
	}
	if err = s.endRecipient("person/standalone", fp, false); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT state FROM outbox WHERE id='sync'`).Scan(&syncState)
	s.db.QueryRow(`SELECT state FROM outbox WHERE id='dm'`).Scan(&dm)
	var selectedState, ownState string
	if err = s.db.QueryRow(`SELECT state FROM outbox WHERE id='former-selected'`).Scan(&selectedState); err != nil {
		t.Fatal(err)
	}
	if err = s.db.QueryRow(`SELECT state FROM outbox WHERE id='own-carrier'`).Scan(&ownState); err != nil {
		t.Fatal(err)
	}
	var prepackState, prepackBody, prepackCipher string
	if err = s.db.QueryRow(`SELECT state,body,envelope FROM outbox WHERE id='own-prepack'`).Scan(&prepackState, &prepackBody, &prepackCipher); err != nil {
		t.Fatal(err)
	}
	if prepackState != stateNotDelivered || prepackBody != "kept history" || prepackCipher != "exact-prepack" {
		t.Fatal("legacy own prepack did not end with its bytes retained")
	}
	if selectedState != stateQueued || ownState != stateNotDelivered {
		t.Fatalf("former-own selected=%s own carrier=%s", selectedState, ownState)
	}
	if syncState != stateNotDelivered || dm != stateQueued {
		t.Fatalf("departed sync=%s standalone DM=%s", syncState, dm)
	}
	t.Run("legacy-unbound-history", func(t *testing.T) {
		w := newWorld(t, "")
		if _, err := w.alice.CreatePerson(tctx(t), "owner"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.alice.store.db.Exec(`INSERT INTO outbox(id,recipient,sub,body,envelope,state,created_at) VALUES('unbound','foreign/device','history','selected','exact','queued',1)`); err != nil {
			t.Fatal(err)
		}
		if err := w.alice.reconcileDepartedHistory(); err != nil {
			t.Fatalf("legacy unbound carrier blocked history: %v", err)
		}
		var state string
		if err := w.alice.store.db.QueryRow(`SELECT state FROM outbox WHERE id='unbound'`).Scan(&state); err != nil || state != stateQueued {
			t.Fatalf("legacy state=%s err=%v", state, err)
		}
	})

}

func TestRecipientLifecycleKnownRevokedSenderCannotStartNewWork(t *testing.T) {
	w := newWorld(t, "")
	a, sender := w.alice, w.bob
	fp := sender.Self().Fingerprint()
	if err := a.store.pin(sender.Self()); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, state string
		replica   int
	}{{"pending", stateAccepted, 0}, {"running", stateRunning, 0}, {"completed", stateAnswered, 0}, {"history", stateAccepted, 1}} {
		if _, err := a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by,replica) VALUES(?,?,1,'question','retained',1,?,?,?)`, row.id, sender.Address, row.state, fp, row.replica); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Revoke(tctx(t), sender.Address); err != nil {
		t.Fatal(err)
	}
	view, _ := json.Marshal(protocol.Members{Members: []protocol.Member{{Address: a.Address, Presence: protocol.PresenceOffline}}})
	a.onMembers(view)
	if err := a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	check := func(id, want string) {
		var got string
		if err := a.store.db.QueryRow(`SELECT state FROM inbox WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("%s=%s want=%s err=%v", id, got, want, err)
		}
	}
	check("pending", stateNotRun)
	check("running", stateRunning)
	check("completed", stateAnswered)
	check("history", stateAccepted)
	// A later local acceptance cannot undo the exact-key denial at claim time.
	if _, err := a.store.db.Exec(`UPDATE inbox SET state='accepted' WHERE id='pending'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.store.claimJob("stub"); err != nil || ok {
		t.Fatalf("claim=%v err=%v", ok, err)
	}
	check("pending", stateNotRun)
	r, err := age.ParseX25519Recipient(a.Self().BoxRecipient)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Seal(envelope.Inner{ID: protocol.NewID(), From: sender.Address, To: a.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion, Body: "must not run"}, sender.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, env.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("revoked pinned request admitted=%d err=%v", n, err)
	}
	// A still-fresh direct-server authorization cache must not bypass denial.
	d := &directServer{a: a, members: map[string]member{sender.Address: {key: sender.Self().SignKey, checked: time.Now()}}}
	if _, err = d.memberKey(tctx(t), sender.Address); err == nil {
		t.Fatal("direct cached sender remained authorized")
	}
	denied, err := revokedDeviceKnown(a.store.db, sender.Address, fp)
	if err != nil || !denied {
		t.Fatalf("durable denial=%v %v", denied, err)
	}
	// Reloading the same database retains the denial, without a process cache.
	reloaded, err := openStore(filepath.Join(a.home, "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.db.Close()
	denied, err = revokedDeviceKnown(reloaded.db, sender.Address, fp)
	if err != nil || !denied {
		t.Fatalf("restart denial=%v %v", denied, err)
	}
}

func TestRecipientLifecycleRequiresExactRevocationNotAbsence(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	target := w.bob
	if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES('pending',?,?,'original','exact', 'waiting',1)`, target.Address, target.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	// Even a complete list missing an enrolled offline device is not proof.
	set := func(truncated bool) {
		actual, err := a.Members(tctx(t))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(protocol.Members{DiscoveryGeneration: actual.DiscoveryGeneration, Members: []protocol.Member{{Address: a.Address, Presence: protocol.PresenceOffline}}, Truncated: truncated})
		a.onMembers(b)
	}
	set(true)
	if err := a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	check := func(want string) {
		var state string
		if err := a.store.db.QueryRow(`SELECT state FROM outbox WHERE id='pending'`).Scan(&state); err != nil || state != want {
			t.Fatalf("state=%s want=%s err=%v", state, want, err)
		}
	}
	check(stateConvWaiting)
	if err := a.Revoke(tctx(t), target.Address); err != nil {
		t.Fatal(err)
	}
	set(true)
	if err := a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	check(stateNotDelivered)
	set(false)
	if err := a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	check(stateNotDelivered)
	if err := a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	check(stateNotDelivered)
}

func TestRecipientLifecycleRevokedOwnReaderDoesNotRegenerateCopies(t *testing.T) {
	w, phone, conv, _ := historyCatchupFixture(t, 1)
	a := w.alice
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	_, root := rootOf(t, a, conv)
	prepared := outCopy{env: envelope.Envelope{To: phone.Address}, in: envelope.Inner{Root: root}, recipientFP: phone.Self().Fingerprint()}
	if err := a.Revoke(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	m, err := a.Members(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	a.onMembers(raw)
	if err = a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	// Preparation preceded learning revocation; the final transaction must
	// reject even a DM copy that has no group authority checks to perform.
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	err = a.checkHistoryCopies(tx, []outCopy{prepared})
	tx.Rollback()
	if !errors.Is(err, ErrPeerRevoked) {
		t.Fatalf("prepared DM escaped final revocation check: %v", err)
	}
	own, ok, err := a.store.selfPerson(a.Address)
	if err != nil || !ok || !own.has(phone.Address, phone.Self().Fingerprint()) {
		t.Fatalf("fixture lost stale signed roster: %v %v", ok, err)
	}
	full, err := syncWindowFull(a.store.db, phone.Self())
	if err != nil || !full {
		t.Fatalf("revoked target remained producible: %v %v", full, err)
	}
	if a.ownSyncReader(tctx(t), phone.Self(), "", map[string]bool{}) {
		t.Fatal("revoked own reader remained active")
	}
	var before int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=?`, phone.Address).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		a.stepByHand(tctx(t))
		for _, produce := range []func() (bool, error){a.syncDeviceHistory, a.syncRoots, a.syncReadMarks, a.syncTopicTitles, a.syncModelReports} {
			if _, err = produce(); err != nil {
				t.Fatal(err)
			}
		}
	}
	var after int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=?`, phone.Address).Scan(&after); err != nil || after != before {
		t.Fatalf("regenerated copies: before=%d after=%d err=%v", before, after, err)
	}
}

func TestRecipientLifecycleLateReceiptPreservesProvenRetention(t *testing.T) {
	t.Run("ordinary_request_handover", testLifecycleReceiptRequiresPossibleRequestHandover)
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	insert := func(id, sub string) {
		t.Helper()
		if _, err = s.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,sub,body,envelope,state,created_at) VALUES(?,'person/phone','key',?,'original','ciphertext','queued',1)`, id, sub); err != nil {
			t.Fatal(err)
		}
	}
	insert("expired", "history")
	insert("delivered", "history")
	insert("manifest", "history-archive")
	insert("child", "history")
	if _, err = s.db.Exec(`UPDATE outbox SET state=? WHERE id='child'`, archiveStaged); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO history_archive_entries VALUES('child','manifest')`); err != nil {
		t.Fatal(err)
	}
	if err = s.endRecipient("person/phone", "key", true); err != nil {
		t.Fatal(err)
	}
	for i, row := range []struct{ id, state string }{{"expired", protocol.StateExpired}, {"delivered", protocol.StateDelivered}, {"manifest", protocol.StateDelivered}} {
		if err = s.applyReceipt(protocol.ReceiptEvent{ID: row.id, State: row.state, Seq: int64(i + 1)}); err != nil {
			t.Fatal(err)
		}
		var state string
		if err = s.db.QueryRow(`SELECT state FROM outbox WHERE id=?`, row.id).Scan(&state); err != nil || state != row.state {
			t.Fatalf("%s=%s want=%s err=%v", row.id, state, row.state, err)
		}
	}
	var child string
	if err = s.db.QueryRow(`SELECT state FROM outbox WHERE id='child'`).Scan(&child); err != nil || child != archiveAccepted {
		t.Fatalf("proven ciphertext retained=%s err=%v", child, err)
	}
}

func TestRecipientLifecycleTruncatedDiscoveryBoundedAndFair(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	tx, err := a.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		_, err = tx.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES(?,?,?,'original','exact','waiting',1)`, fmt.Sprintf("pending-%04d", i), fmt.Sprintf("admin/target-%04d", i), "captured-key")
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	calls := 0
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		seen[r.URL.Path] = true
		// A successful signed nonrevoked response is memoized for this round.
		public := a.id.Public(strings.TrimPrefix(r.URL.Path, "/v1/agents/"))
		raw, _ := json.Marshal(protocol.DirectoryEntry{Public: public})
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
	})
	members, _ := json.Marshal(protocol.Members{Members: []protocol.Member{{Address: a.Address, Presence: protocol.PresenceOffline}}, Truncated: true})
	for pass := 0; pass < 22; pass++ {
		// Continuous member events must not restart the round and starve its tail.
		var updated protocol.Members
		if err = json.Unmarshal(members, &updated); err != nil {
			t.Fatal(err)
		}
		updated.DiscoveryGeneration = uint64(pass + 1)
		event, _ := json.Marshal(updated)
		a.onMembers(event)
		before := calls
		if err = a.reconcileRecipientLifecycle(tctx(t)); err != nil {
			t.Fatal(err)
		}
		if calls-before > proofPage {
			t.Fatalf("unbounded pass: %d", calls-before)
		}
	}
	if len(seen) < 1000 {
		t.Fatalf("tail starved: only %d exact targets inspected", len(seen))
	}
	var pending int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE id LIKE 'pending-%' AND state='waiting'`).Scan(&pending); err != nil || pending != 1000 {
		t.Fatalf("absence/network failure ended copies: pending=%d err=%v", pending, err)
	}
	// Finish the coalesced latest round; with no new event it must quiesce.
	for pass := 0; pass < 42; pass++ {
		if err = a.reconcileRecipientLifecycle(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	before := calls
	if err = a.reconcileRecipientLifecycle(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if calls != before {
		t.Fatalf("completed discovery round repeated without new evidence: before=%d after=%d", before, calls)
	}
}

func TestRecipientLifecycleForcedGenerationWithPresenceChange(t *testing.T) {
	w := newWorld(t, "")
	a, target := w.alice, w.bob
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES('pending',?,?,'original','exact','waiting',1)`, target.Address, target.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	base := a.hub.http.Transport
	directoryCalls := 0
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/agents/bob/laptop" {
			directoryCalls++
		}
		return base.RoundTrip(r)
	})
	event := func(presence string) {
		actual, err := a.Members(tctx(t))
		if err != nil {
			t.Fatal(err)
		}
		actual.Members = []protocol.Member{{Address: a.Address, Presence: presence}}
		actual.Truncated = true
		b, _ := json.Marshal(actual)
		a.onMembers(b)
		if err = a.reconcileRecipientLifecycle(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	event(protocol.PresenceOffline)
	before := directoryCalls
	event(protocol.PresenceConnected)
	if directoryCalls != before {
		t.Fatal("presence-only event repeated exact directory probes")
	}
	if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES('new-key',?,'different-key','original','exact','waiting',1)`, target.Address); err != nil {
		t.Fatal(err)
	}
	event(protocol.PresenceConnected)
	if directoryCalls != before+1 {
		t.Fatal("new exact open target was hidden by completed discovery round")
	}
	before = directoryCalls
	if err := a.Revoke(tctx(t), target.Address); err != nil {
		t.Fatal(err)
	}
	// Forced directory change and presence change can share one snapshot.
	event(protocol.PresenceOffline)
	var state string
	if err := a.store.db.QueryRow(`SELECT state FROM outbox WHERE id='pending'`).Scan(&state); err != nil || state != stateNotDelivered {
		t.Fatalf("forced lookup lost behind presence change: state=%s err=%v", state, err)
	}
	if directoryCalls != before+2 {
		t.Fatalf("forced exact probes=%d want=%d", directoryCalls, before+2)
	}
	if err := a.store.db.QueryRow(`SELECT state FROM outbox WHERE id='new-key'`).Scan(&state); err != nil || state != stateConvWaiting {
		t.Fatalf("different key incorrectly ended: state=%s err=%v", state, err)
	}
}

func TestRecipientLifecycleFailedDirectoryRetriesOnExistingWake(t *testing.T) {
	w := newWorld(t, "")
	a, target := w.alice, w.bob
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES('pending',?,?,'original','exact','waiting',1)`, target.Address, target.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES('legacy-in',?,1,'question','original',1,'pending',NULL)`, target.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES('legacy-out',?,NULL,'original','exact','waiting',1)`, target.Address); err != nil {
		t.Fatal(err)
	}
	if err := a.Revoke(tctx(t), target.Address); err != nil {
		t.Fatal(err)
	}
	actual, err := a.Members(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	actual.Members = []protocol.Member{{Address: a.Address, Presence: protocol.PresenceOffline}}
	actual.Truncated = true
	event, _ := json.Marshal(actual)
	a.onMembers(event)
	a.convWork.take()
	base := a.hub.http.Transport
	attempts := 0
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/agents/bob/laptop" {
			attempts++
			if attempts == 1 {
				return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(`{"error":"temporarily unavailable"}`)), Header: make(http.Header)}, nil
			}
		}
		return base.RoundTrip(r)
	})
	wakes := make(chan struct{}, 1)
	a.kickMu.Lock()
	a.kick = func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	}
	a.kickMu.Unlock()
	cancelled, cancel := context.WithCancel(tctx(t))
	cancel()
	if err = a.reconcileRecipientLifecycle(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled storage read: %v", err)
	}
	if a.convWork.bits.Load()&convRecipients == 0 {
		t.Fatal("failed storage read lost due discovery work")
	}
	a.convSyncByHand(tctx(t))
	var state string
	if err = a.store.db.QueryRow(`SELECT state FROM outbox WHERE id='pending'`).Scan(&state); err != nil || state != stateConvWaiting {
		t.Fatalf("failed lookup ended work: state=%s err=%v", state, err)
	}
	if a.convWork.bits.Load()&convRecipients == 0 {
		t.Fatal("failed lookup lost retry work bit")
	}
	select {
	case <-wakes:
		t.Fatal("failed lookup immediately kicked retry")
	default:
	}
	// Existing signed ping/event wake takes the retained work; it adds no bit.
	a.kickNow()
	<-wakes
	a.convSyncByHand(tctx(t))
	if a.convWork.bits.Load()&convRecipients != 0 {
		t.Fatal("successful retry remained due")
	}
	if err = a.store.db.QueryRow(`SELECT state FROM outbox WHERE id='pending'`).Scan(&state); err != nil || state != stateNotDelivered || attempts != 2 {
		t.Fatalf("same-generation retry lost: state=%s attempts=%d err=%v", state, attempts, err)
	}
}

func TestRecipientLifecycleDefinitiveDirectoryFailureIsGenerationBound(t *testing.T) {
	for _, mode := range []string{"not found", "forbidden", "expired", "wrong address", "invalid signature"} {
		t.Run(mode, func(t *testing.T) {
			w := newWorld(t, "")
			a, target := w.alice, w.bob
			a.stopBackgroundPosts()
			a.stopArchivePosts()
			if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES('pending',?,?,'original','exact','waiting',1)`, target.Address, target.Self().Fingerprint()); err != nil {
				t.Fatal(err)
			}
			members := protocol.Members{Members: []protocol.Member{{Address: a.Address, Presence: protocol.PresenceOffline}}, Truncated: true, DiscoveryGeneration: 1}
			publish := func() { b, _ := json.Marshal(members); a.onMembers(b); a.convWork.take() }
			publish()
			base := a.hub.http.Transport
			attempts := 0
			a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v1/agents/bob/laptop" {
					return base.RoundTrip(r)
				}
				attempts++
				status := http.StatusOK
				raw := []byte(`{"error":"unavailable"}`)
				switch mode {
				case "not found":
					status = http.StatusNotFound
				case "forbidden":
					status = http.StatusForbidden
				case "expired":
					status = http.StatusGone
				default:
					public := target.Self()
					if mode == "wrong address" {
						public = a.Self()
					} else {
						public.BoxSig[0] ^= 1
					}
					raw, _ = json.Marshal(protocol.DirectoryEntry{Public: public, Revoked: true})
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(raw))), Header: make(http.Header)}, nil
			})
			for i := 0; i < 2; i++ {
				a.convWork.due(convRecipients)
				a.convSyncByHand(tctx(t))
				if a.convWork.bits.Load()&convRecipients != 0 {
					t.Fatal("definitive invalid response scheduled ping retries")
				}
			}
			if attempts != 1 {
				t.Fatalf("definitive failure probes=%d want1", attempts)
			}
			members.DiscoveryGeneration++
			publish()
			a.convWork.due(convRecipients)
			a.convSyncByHand(tctx(t))
			if attempts != 2 {
				t.Fatal("directory change did not invalidate definitive failure memo")
			}
			var state string
			if err := a.store.db.QueryRow(`SELECT state FROM outbox WHERE id='pending'`).Scan(&state); err != nil || state != stateConvWaiting {
				t.Fatalf("invalid response claimed permanent revocation: state=%s err=%v", state, err)
			}
		})
	}
}

func TestRecipientLifecycleFailedFirstPageRetriesAfterRound(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	for i := 0; i < proofPage+1; i++ {
		address := fmt.Sprintf("probe/device%03d", i)
		key := a.id.Public(address)
		if _, err := a.store.db.Exec(`INSERT INTO outbox(id,recipient,recipient_fp,body,envelope,state,created_at) VALUES(?,?,?,'original','exact','waiting',1)`, fmt.Sprint(i), address, key.Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	members, err := a.Members(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	members.Members = []protocol.Member{{Address: a.Address, Presence: protocol.PresenceOffline}}
	members.Truncated = true
	raw, _ := json.Marshal(members)
	a.onMembers(raw)
	a.convWork.take()
	attempts := map[string]int{}
	base := a.hub.http.Transport
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasPrefix(r.URL.Path, "/v1/agents/probe/") {
			return base.RoundTrip(r)
		}
		address := strings.TrimPrefix(r.URL.Path, "/v1/agents/")
		attempts[address]++
		status := http.StatusOK
		body, _ := json.Marshal(protocol.DirectoryEntry{Public: a.id.Public(address)})
		if address == "probe/device000" && attempts[address] == 1 {
			status = http.StatusServiceUnavailable
			body = []byte(`{"error":"temporary"}`)
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})
	a.convWork.due(convRecipients)
	a.convSyncByHand(tctx(t)) // first page, failed first target
	a.convSyncByHand(tctx(t)) // continuation consumes prior due bit
	if a.convWork.bits.Load()&convRecipients == 0 {
		t.Fatal("later successful page lost first-page retry")
	}
	a.kickNow()
	a.convSyncByHand(tctx(t)) // existing ping/event wake, no added work bit
	if attempts["probe/device000"] != 2 {
		t.Fatalf("failed key attempts=%d", attempts["probe/device000"])
	}
	for address, n := range attempts {
		if address != "probe/device000" && n != 1 {
			t.Fatalf("successful key reprobed: %s %d", address, n)
		}
	}
	if a.convWork.bits.Load()&convRecipients != 0 {
		t.Fatal("successful recovery remained due")
	}
}
