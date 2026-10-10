package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

func archiveFixture(t *testing.T, n int) (*world, *Agent, *Agent, string) {
	t.Helper()
	w, phone, conv, _ := historyCatchupFixture(t, n)
	stop := runAgent(t, phone)
	label, name, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "phone archive capability", func() bool {
		var profile protocol.Profile
		err := phone.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile)
		return err == nil && profile.Supports(phone.Address, phone.Self().SignKey, protocol.CapHistoryArchive)
	})
	stop()
	a := w.alice
	for _, p := range []*backgroundPosts{&a.archivePosting, &a.archiveImporting} {
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
	// The world also has Bob's bootstrap wakes. This fixture qualifies one
	// exact linked-phone source, not unrelated synthetic recipients.
	if _, err := a.store.db.Exec(`DELETE FROM outbox WHERE state=? OR sub=?`, archiveStaged, envelope.SubHistoryArchive); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DELETE FROM history_catchup WHERE device=?`, `DELETE FROM history_copies WHERE recipient_fp=?`, `DELETE FROM history_deferred WHERE recipient_fp=?`} {
		key := phone.Self().Fingerprint()
		if query == `DELETE FROM history_catchup WHERE device=?` {
			key = phone.Address
		}
		if _, err := a.store.db.Exec(query, key); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := phone.store.pin(a.Self()); err != nil {
		t.Fatal(err)
	}
	return w, a, phone, conv
}

func archiveDescriptor(t *testing.T, a *Agent) envelope.Envelope {
	t.Helper()
	var raw string
	if err := a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? ORDER BY rowid DESC LIMIT 1`, envelope.SubHistoryArchive).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestHistoryArchiveCanceledStreamHandsOffNewWake(t *testing.T) {
	a := &Agent{}
	p := &backgroundPosts{}
	old, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, resumed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls int
	step := func(ctx context.Context) (bool, error) {
		calls++
		if calls == 1 {
			close(entered)
			<-release
			return false, ctx.Err()
		}
		if ctx.Err() != nil {
			t.Error("new wake inherited canceled old stream")
		}
		close(resumed)
		return false, nil
	}
	a.archiveWake(old, p, step)
	<-entered
	cancel()
	a.archiveWake(context.Background(), p, step)
	close(release)
	select {
	case <-resumed:
	case <-time.After(time.Second):
		t.Fatal("new stream wake was lost behind canceled worker")
	}
	p.Lock()
	done := p.done
	p.Unlock()
	if done != nil {
		<-done
	}
	called := make(chan struct{})
	a.archiveWake(old, p, func(context.Context) (bool, error) {
		close(called)
		return false, nil
	})
	select {
	case <-called:
		t.Fatal("canceled parent started archive work after transport stop")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestHistoryArchiveLegacyCustodyMigrationExactAndOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := sqlitedb.Open(path, schema[:len(schema)-1])
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, sub string }{{"background", "history"}, {"turn", ""}, {"proof", "group-proof"}} {
		if _, err = db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,sub) VALUES(?,?,?,?,?,?,?)`, row.id, "admin/phone", "retained body", "exact signed ciphertext", protocol.StateCustody, 123, row.sub); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	db, err = sqlitedb.Open(path, schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, state string }{{"background", stateQueued}, {"turn", protocol.StateCustody}, {"proof", protocol.StateCustody}} {
		var state, wire, body string
		var created int
		if err = db.QueryRow(`SELECT state,envelope,body,created_at FROM outbox WHERE id=?`, row.id).Scan(&state, &wire, &body, &created); err != nil || state != row.state || wire != "exact signed ciphertext" || body != "retained body" || created != 123 {
			t.Fatalf("migration changed retained %s: %s %q %q %d %v", row.id, state, wire, body, created, err)
		}
	}
	if _, err = db.Exec(`UPDATE outbox SET state='custody' WHERE id='background'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = sqlitedb.Open(path, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err = db.QueryRow(`SELECT state FROM outbox WHERE id='background'`).Scan(&state); err != nil || state != protocol.StateCustody {
		t.Fatal("custody reannouncement repeated on restart", state, err)
	}
}

func TestHistoryArchiveCapabilityLossKeepsExactManifestAcrossRestart(t *testing.T) {
	_, a, phone, _ := archiveFixture(t, 3)
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.archiveStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	env := archiveDescriptor(t, a)
	label, name, _ := protocol.SplitAddress(phone.Address)
	var profile protocol.Profile
	if err := phone.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile); err != nil {
		t.Fatal(err)
	}
	var records []protocol.CapsRecord
	for _, raw := range profile.Caps {
		var record protocol.CapsRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
		caps := []string{}
		for _, cap := range record.Caps {
			if cap != protocol.CapHistoryArchive {
				caps = append(caps, cap)
			}
		}
		record.Caps, record.TS = caps, record.TS+1
		record.Sign(phone.id.Sign)
		if err := phone.hub.do(tctx(t), "PUT", "/v1/caps", record, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(records) == 0 {
		t.Fatal("fixture lacks signed phone capability record")
	}
	if err := a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	state, _, _, err := a.store.outboxState(env.ID)
	if err != nil || state != stateConvWaiting {
		t.Fatalf("archive capability loss did not retain waiting descriptor: %s %v", state, err)
	}
	home := a.home
	a.Close()
	a, err = Open(home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err = a.archivePack(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE sub=?`, envelope.SubHistoryArchive).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restart resealed pending manifest: %d %v", count, err)
	}
	for _, record := range records {
		record.TS += 2
		record.Sign(phone.id.Sign)
		if err = phone.hub.do(tctx(t), "PUT", "/v1/caps", record, nil); err != nil {
			t.Fatal(err)
		}
	}
	features, err := a.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	a.releaseConv(tctx(t), features)
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	state, _, _, err = a.store.outboxState(env.ID)
	if err != nil || state != protocol.StateCustody {
		t.Fatalf("restored capability did not release exact descriptor: %s %v", state, err)
	}
}

func TestHistoryArchiveReceiptDrivenMultiChunkCompletion(t *testing.T) {
	_, a, phone, conv := archiveFixture(t, 123)
	for page := int64(1); page <= 4; page++ {
		if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
		if _, err := a.archiveStep(tctx(t)); err != nil {
			t.Fatal(err)
		}
		env := archiveDescriptor(t, a)
		if err := a.FlushOutbox(tctx(t)); err != nil {
			t.Fatal(err)
		}
		if err := phone.storeReceived(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if _, err := phone.archiveImportStep(tctx(t)); err != nil {
			t.Fatal(err)
		}
		if err := phone.flushReceipts(tctx(t)); err != nil {
			t.Fatal(err)
		}
		r, err := a.Status(tctx(t), env.ID, 0)
		if err != nil || r.State != protocol.StateDelivered {
			t.Fatalf("descriptor lacks proven retention receipt: %+v %v", r, err)
		}
		if err = a.store.applyReceipt(protocol.ReceiptEvent{ID: env.ID, State: r.State, Seq: page}); err != nil {
			t.Fatal(err)
		}
		messages, err := phone.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, m := range messages {
			if len(m.Body) >= 4 && m.Body[:4] == "row-" {
				n++
			}
		}
		if n == 123 {
			return
		}
	}
	t.Fatal("receipt-driven bounded archives did not converge")
}

func TestHistoryArchiveTenThousandSourcesBoundedNewestFirst(t *testing.T) {
	_, a, phone, conv := archiveFixture(t, 1)
	// This test drives every source send synchronously. Join and disable the
	// fixture's leftover coalesced sender before installing its recorder;
	// queued source wakes must not read the HTTP client concurrently.
	a.stopBackgroundPosts()
	a.stopArchivePosts()
	var original string
	if err := a.store.db.QueryRow(`SELECT id FROM inbox WHERE conv=? AND body='row-000'`, conv).Scan(&original); err != nil {
		t.Fatal(err)
	}
	// Synthetic accepted source rows in one transaction: this measures the
	// 10k-row selector without signing/encrypting the entire corpus first.
	_, err := a.store.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv,lid,origin,received_ms,verified_by) SELECT lower(hex(randomblob(16))),i.sender,i.ts,i.kind,'bulk-'||n.x,i.received_at,i.conv,lower(hex(randomblob(16))),i.origin,n.x+10000,i.verified_by FROM n JOIN inbox i ON i.id=?`, original)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE state=?`, archiveStaged).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != historyPage {
		t.Fatalf("prebuilt corpus instead of one source page: %d", count)
	}
	if err = a.archivePack(tctx(t)); err != nil {
		t.Fatal(err)
	}
	first := archiveDescriptor(t, a)
	for range 8 {
		if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE state=?`, archiveStaged).Scan(&count); err != nil || count != historyPage {
		t.Fatalf("full chunk produced more source ciphertext: %d %v", count, err)
	}
	base := a.hub.http.Transport
	var posted []string
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/messages" {
			var env envelope.Envelope
			b, _ := r.GetBody()
			err := json.NewDecoder(b).Decode(&env)
			b.Close()
			if err != nil {
				return nil, err
			}
			posted = append(posted, env.ID)
		}
		return base.RoundTrip(r)
	})
	defer func() { a.hub.http.Transport = base }()
	var before string
	a.store.db.QueryRow(`SELECT group_concat(id||':'||state||':'||coalesce(recipient_fp,'')) FROM outbox WHERE sub=?`, envelope.SubHistoryArchive).Scan(&before)
	if _, err = a.archiveStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err = a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if len(posted) != 1 || posted[0] != first.ID {
		var state, why string
		a.store.db.QueryRow(`SELECT state,coalesce(error,'') FROM outbox WHERE id=?`, first.ID).Scan(&state, &why)
		var exportError string
		a.store.db.QueryRow(`SELECT error FROM history_archive_export_errors LIMIT 1`).Scan(&exportError)
		why += exportError
		t.Fatalf("per-entry bootstrap relay posts: %v; descriptor=%s %s; before=%s", posted, state, why, before)
	}
	if err = phone.storeReceived(tctx(t), first); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.archiveImportStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err = a.store.applyReceipt(protocol.ReceiptEvent{ID: first.ID, State: protocol.StateDelivered, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	if err = a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE state=?`, archiveAccepted).Scan(&count); err != nil || count != historyPage {
		t.Fatalf("retained descriptor did not retire pending children: %d %v", count, err)
	}
	if full, e := syncWindowFull(a.store.db, phone.Self()); e != nil || full {
		t.Fatalf("completed mapping still filled pending window: %v %v", full, e)
	}
	if _, err = a.store.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<10000) INSERT INTO outbox(id,recipient,body,envelope,state,created_at,sub,recipient_fp) SELECT lower(hex(randomblob(16))),recipient,body,envelope,?,created_at,sub,recipient_fp FROM n JOIN outbox o ON o.id=(SELECT child FROM history_archive_entries WHERE manifest=? LIMIT 1)`, archiveAccepted, first.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := a.store.db.Query("EXPLAIN QUERY PLAN "+syncWindowQuery, historyPage, phone.Address, phone.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	var indexed int
	for plan.Next() {
		var id, parent, unused int
		var detail string
		if err = plan.Scan(&id, &parent, &unused, &detail); err != nil {
			plan.Close()
			t.Fatal(err)
		}
		if strings.Contains(detail, "outbox_pending_exact") {
			indexed++
		}
	}
	plan.Close()
	if indexed != 2 {
		t.Fatalf("pending window queries did not both use bounded state index: %d", indexed)
	}
	checked := time.Now()
	for range 100 {
		if full, e := syncWindowFull(a.store.db, phone.Self()); e != nil || full {
			t.Fatalf("10k retained rows affected pending window: %v %v", full, e)
		}
	}
	t.Logf("10k retained outbox rows: 100 indexed pending-window checks=%s", time.Since(checked))
	messages, err := phone.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var latest bool
	for _, m := range messages {
		latest = latest || m.Body == "bulk-10000"
	}
	if !latest {
		t.Fatal("archive omitted newest source page")
	}
	var childReceipts int
	receipts, err := phone.store.unsentReceipts()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range receipts {
		var isChild bool
		if err = phone.store.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM history_archive_children WHERE id=?)`, r.id).Scan(&isChild); err != nil {
			t.Fatal(err)
		}
		if isChild {
			childReceipts++
		}
	}
	if childReceipts != 0 {
		t.Fatal("fabricated relay receipts for imported children")
	}
}

func TestHistoryArchiveBlockedUploadDoesNotBlockSamePhoneLive(t *testing.T) {
	w, a, phone, conv := archiveFixture(t, 55)
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	base := a.hub.http.Transport
	blocked, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/blobs" {
			once.Do(func() { close(blocked) })
			select {
			case <-release:
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
		}
		return base.RoundTrip(r)
	})
	defer func() { a.hub.http.Transport = base }()
	done := make(chan error, 1)
	go func() { _, err := a.archiveStep(tctx(t)); done <- err }()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("archive upload did not start")
	}
	start := time.Now()
	sent, err := a.Send(WithQueuedSend(context.Background(), protocol.NewID()), phone.Address, "fresh live gesture to same phone", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		state, _, _, _ := a.store.outboxState(sent.ID)
		if state == protocol.StateCustody {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("live message did not reach custody")
		}
		time.Sleep(time.Millisecond)
	}
	if time.Since(start) > time.Second {
		t.Fatal("live custody waited behind archive upload")
	}
	custodyLatency := time.Since(start)
	var raw string
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, sent.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var live envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &live); err != nil {
		t.Fatal(err)
	}
	if err = phone.storeReceived(tctx(t), live); err != nil || !inboxHas(t, phone, sent.ID) {
		t.Fatal("phone did not admit actual live turn while archive upload held", err)
	}
	admissionLatency := time.Since(start)
	t.Logf("blocked bootstrap upload: same-phone custody=%s signed local admission=%s", custodyLatency, admissionLatency)
	if admissionLatency > 3*time.Second {
		t.Fatal("live admission waited behind blocked bootstrap upload")
	}
	_, root := rootOf(t, w.bob, conv)
	lid := protocol.NewID()
	incoming := craft(t, w.bob, a, envelope.Inner{Kind: envelope.KindMessage, Body: "fresh mirrored reply", Conv: conv, Root: root, LID: lid, Origin: envelope.OriginUI})
	if err = a.verifyAndStore(tctx(t), incoming); err != nil {
		t.Fatal(err)
	}
	if _, err = a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if err = a.store.db.QueryRow(`SELECT o.envelope FROM outbox o JOIN history_copies h ON h.carrier=o.id WHERE o.recipient=? AND h.lid=? AND o.replication_live=1`, phone.Address, lid).Scan(&raw); err != nil {
		t.Fatal("fresh same-phone mirror was blocked by cold archive window", err)
	}
	if err = a.flushOutbox(tctx(t), false); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(raw), &live); err != nil {
		t.Fatal(err)
	}
	if err = phone.storeReceived(tctx(t), live); err != nil {
		t.Fatal("same-phone fresh mirror failed actual admission", err)
	}
	messages, err := phone.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if m.Body == "fresh mirrored reply" {
			return
		}
	}
	t.Fatal("phone did not receive fresh mirrored reply while archive held")
}

func TestHistoryArchiveDescriptorNoIOUntilCiphertextRetained(t *testing.T) {
	_, a, phone, _ := archiveFixture(t, 3)
	if _, err := a.historyCatchupPage(tctx(t), phone.Self()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.archiveStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := a.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	env := archiveDescriptor(t, a)
	base := phone.hub.http.Transport
	phone.hub.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("synthetic network blocked") })
	if err := phone.storeReceived(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	var retained int
	if err := phone.store.db.QueryRow(`SELECT retained FROM history_archive_jobs WHERE id=?`, env.ID).Scan(&retained); err != nil || retained != 0 {
		t.Fatalf("dispatch performed blob work: %d %v", retained, err)
	}
	if _, err := phone.archiveImportStep(tctx(t)); err == nil {
		t.Fatal("blocked download unexpectedly succeeded")
	}
	var receipt int
	if err := phone.store.db.QueryRow(`SELECT count(*) FROM history_receipts WHERE id=?`, env.ID).Scan(&receipt); err != nil || receipt != 0 {
		t.Fatalf("descriptor acked before ciphertext durable: %d %v", receipt, err)
	}
	phone.hub.http.Transport = base
	if _, err := phone.store.db.Exec(`UPDATE history_archive_jobs SET error='' WHERE id=?`, env.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.archiveImportStep(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := phone.storeReceived(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	var done int
	if err := phone.store.db.QueryRow(`SELECT done FROM history_archive_jobs WHERE id=?`, env.ID).Scan(&done); err != nil || done != 1 {
		t.Fatal("duplicate descriptor reset completed import", err)
	}
	if _, err := os.Stat(phone.downloadPath(env.Blobs[0].ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("completed archive ciphertext retained", err)
	}
	var raw string
	if err := a.store.db.QueryRow(`SELECT o.envelope FROM history_archive_entries e JOIN outbox o ON o.id=e.child WHERE e.manifest=? LIMIT 1`, env.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var child envelope.Envelope
	if err := json.Unmarshal([]byte(raw), &child); err != nil {
		t.Fatal(err)
	}
	tampered := child
	tampered.CT = append([]byte(nil), child.CT...)
	tampered.CT[0] ^= 1
	if err := phone.storeReceived(tctx(t), tampered); err == nil {
		t.Fatal("tampered imported-child arrival enabled relay ACK")
	}
	if err := phone.storeReceived(tctx(t), child); err != nil {
		t.Fatal("exact imported child relay overlap refused", err)
	}
	receipts, err := phone.store.unsentReceipts()
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		if receipt.id == child.ID {
			return
		}
	}
	t.Fatal("actual exact child relay arrival remained ACK-suppressed")
}
