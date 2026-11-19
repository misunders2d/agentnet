package client

import (
	"database/sql"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type reviewSnapshotTransport func(*http.Request) (*http.Response, error)

func (f reviewSnapshotTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReviewAttentionCoalescesConcurrentRunWakes(t *testing.T) {
	w := newWorld(t, "")
	insert := func() {
		t.Helper()
		if _, err := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, protocol.NewID(), w.alice.Address, 1, envelope.KindQuestion, "PRIVATE", 1, stateHeld, w.alice.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
	}
	insert()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	w.bob.nativeNotify = func(string) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
	}
	go func() { defer close(done); w.bob.reviewAttention(tctx(t)) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	select {
	case <-entered:
	case <-tctx(t).Done():
		t.Fatal("attention pass never reached notifier")
	}
	insert()
	w.bob.NoteChange()
	w.bob.reviewAttention(tctx(t))
	if calls.Load() != 1 {
		t.Fatal("overlapping run wake performed another attention projection")
	}
	for {
		select {
		case <-w.bob.workerWake:
			continue
		default:
		}
		break
	}
	close(release)
	<-done
	select {
	case <-w.bob.workerWake:
	default:
		t.Fatal("changed attention lost its wake while the older pass was busy")
	}
	w.bob.reviewAttention(tctx(t))
	if calls.Load() != 2 {
		t.Fatal("reawakened attention omitted the new held request")
	}
}

func TestReviewSnapshotRechecksChangeWhileSending(t *testing.T) {
	t.Run("visibility_refresh_retains_settlement", testReviewVisibilityRefreshRetainsSettlement)
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	waitHeadlessReviewCaps(t, w.alice)
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	env := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: "synthetic decision"})
	if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	base := w.bob.hub.http.Transport
	w.bob.hub.http.Transport = reviewSnapshotTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" && r.URL.Path == "/v1/agents/admin/alice/profile" {
			once.Do(func() { close(entered); <-release })
		}
		return base.RoundTrip(r)
	})
	done := make(chan struct{})
	go func() { defer close(done); w.bob.sendReviewNotice(tctx(t)) }()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
		<-done
	}()
	select {
	case <-entered:
	case <-tctx(t).Done():
		t.Fatal("review snapshot never reached recipient profile")
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, env.ID); err != nil {
		t.Fatal(err)
	}
	w.bob.NoteChange()
	w.bob.sendReviewNotice(tctx(t)) // existing pass skips the busy sender
	for {
		select {
		case <-w.bob.workerWake:
			continue
		default:
		}
		break
	}
	close(release)
	<-done
	select {
	case <-w.bob.workerWake:
	default:
		t.Fatal("finished request lost its review pass while an older snapshot was being sent")
	}
	w.bob.sendReviewNotice(tctx(t))
	var body string
	if err := w.bob.store.db.QueryRow(`SELECT body FROM outbox WHERE recipient=? AND status=? ORDER BY rowid DESC LIMIT 1`, w.alice.Address, envelope.StatusReviewNotice).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !noticeSettled(body) {
		t.Fatal("reawakened pass did not send the final empty snapshot")
	}
	// Sending the settlement itself causes one existing change event. Its
	// final idle pass must neither create another report nor wake itself.
	select {
	case <-w.bob.workerWake:
	default:
		t.Fatal("settlement did not preserve its committed change wake")
	}
	w.bob.sendReviewNotice(tctx(t))
	select {
	case <-w.bob.workerWake:
		t.Fatal("quiescent review pass woke itself")
	default:
	}
	var notices int
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE recipient=? AND status=?`, w.alice.Address, envelope.StatusReviewNotice).Scan(&notices); err != nil || notices != 2 {
		t.Fatalf("duplicate final reports: count=%d err=%v", notices, err)
	}
}

func logReviewSettlement(t *testing.T, source, reader *Agent) {
	t.Helper()
	t.Logf("source review attempted keys=%v", source.reviewTried)
	for _, query := range []string{`SELECT item,recipient FROM reported`, `SELECT id,state FROM inbox WHERE kind='question'`} {
		rows, err := source.store.db.Query(query)
		if err != nil {
			t.Log(err)
			continue
		}
		for rows.Next() {
			var first, second string
			if err := rows.Scan(&first, &second); err != nil {
				t.Log(err)
				break
			}
			t.Logf("source review ledger=%s %s", first, second)
		}
		rows.Close()
	}
	for _, side := range []struct {
		name  string
		db    *sql.DB
		query string
	}{
		{"source", source.store.db.DB, `SELECT id,created_at,state,body FROM outbox WHERE status='review_notice' ORDER BY rowid`},
		{"reader", reader.store.db.DB, `SELECT id,ts,state,body FROM inbox WHERE status='review_notice' ORDER BY arrival`},
	} {
		rows, err := side.db.Query(side.query)
		if err != nil {
			t.Logf("%s review diagnostics: %v", side.name, err)
			continue
		}
		for rows.Next() {
			var id, state, body string
			var ts int64
			if err := rows.Scan(&id, &ts, &state, &body); err != nil {
				t.Log(err)
				break
			}
			report, structured := ParseReport(body)
			t.Logf("%s review id=%s ts=%d state=%s structured=%v at=%d count=%d items=%d settled=%v", side.name, id, ts, state, structured, report.At, report.Count, len(report.Items), noticeSettled(body))
		}
		rows.Close()
	}
}

func waitHeadlessReviewCaps(t *testing.T, reader *Agent) {
	t.Helper()
	label, name, _ := protocol.SplitAddress(reader.Address)
	eventually(t, "published signed review capability "+reader.Address, func() bool {
		var profile protocol.Profile
		err := reader.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+name+"/profile", nil, &profile)
		return err == nil && profile.Supports(reader.Address, reader.Self().SignKey, protocol.CapHeadless)
	})
}

func TestReviewNoticeCapabilitySelectsSettledReportFormat(t *testing.T) {
	w := newWorld(t, "")
	fakeNotify(w.alice)
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	storeTask := func() string {
		t.Helper()
		env := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: "synthetic decision"})
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	latest := func() string {
		t.Helper()
		var body string
		if err := w.bob.store.db.QueryRow(`SELECT body FROM outbox WHERE recipient=? AND status=? ORDER BY rowid DESC LIMIT 1`, w.alice.Address, envelope.StatusReviewNotice).Scan(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	finish := func(id string) {
		t.Helper()
		if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state=? WHERE id=?`, stateAnswered, id); err != nil {
			t.Fatal(err)
		}
	}
	first := storeTask()
	w.bob.sendReviewNotice(tctx(t)) // no reader session has published capabilities
	if body := latest(); body != countText(1, w.bob.Address) {
		t.Fatalf("unpublished reader did not select legacy count: %q", body)
	}
	finish(first)
	w.bob.sendReviewNotice(tctx(t))
	if body := latest(); body != countText(0, w.bob.Address) || noticeSettled(body) {
		t.Fatalf("legacy count incorrectly claimed structured settlement: %q", body)
	}
	runWith(t, w, w.alice, RunOptions{})
	waitHeadlessReviewCaps(t, w.alice)
	second := storeTask()
	w.bob.sendReviewNotice(tctx(t))
	if report, ok := ParseReport(latest()); !ok || report.Count != 1 {
		t.Fatalf("signed capable reader did not select structured report: %+v %v", report, ok)
	}
	finish(second)
	w.bob.sendReviewNotice(tctx(t))
	if !noticeSettled(latest()) {
		t.Fatal("signed capable reader did not get a structured empty snapshot")
	}
}

func testReviewVisibilityRefreshRetainsSettlement(t *testing.T) {
	w := newWorld(t, "")
	runWith(t, w, w.alice, RunOptions{})
	waitHeadlessReviewCaps(t, w.alice)
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	in := taskFrom(w.alice.Address, w.bob.Address)
	in.Kind = envelope.KindQuestion
	if err := w.bob.store.addInbox(in, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state='running',last_attempt_at=unixepoch() WHERE id=?`, in.ID); err != nil {
		t.Fatal(err)
	}
	w.bob.sendReviewNotice(tctx(t))
	var n int
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM reported WHERE item=? AND recipient=?`, in.ID, w.alice.Address).Scan(&n); err != nil || n != 1 {
		t.Fatalf("running recipient evidence %d %v", n, err)
	}
	if changed, err := w.bob.store.markRunVisibility(in.ID, false); err != nil || !changed {
		t.Fatalf("visibility refresh %v %v", changed, err)
	}
	// Completion wins before the refresh pass: the prior recipient still needs
	// a clear, even though no refreshed running snapshot is necessary anymore.
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET state='answered' WHERE id=?`, in.ID); err != nil {
		t.Fatal(err)
	}
	w.bob.NoteChange()
	w.bob.sendReviewNotice(tctx(t))
	var body string
	if err := w.bob.store.db.QueryRow(`SELECT body FROM outbox WHERE status='review_notice' ORDER BY rowid DESC LIMIT 1`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !noticeSettled(body) {
		t.Fatal("visibility refresh discarded recipient evidence; finished request left running report")
	}
	w.bob.sendReviewNotice(tctx(t))
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE status='review_notice'`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("reports %d %v", n, err)
	}
}
