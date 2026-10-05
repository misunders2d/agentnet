package client

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type noticeRow struct {
	ID, Body, State, Detail string
}

// notices returns the review notices a has received from sender.
func notices(t *testing.T, a *Agent, sender string) []noticeRow {
	t.Helper()
	rows, err := a.store.db.Query(`SELECT id, body, coalesce(state, ''), coalesce(detail, '') FROM inbox
		WHERE sender = ? AND kind = ? AND status = ? ORDER BY arrival`, sender, envelope.KindMessage, envelope.StatusReviewNotice)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []noticeRow
	for rows.Next() {
		var n noticeRow
		if err := rows.Scan(&n.ID, &n.Body, &n.State, &n.Detail); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func reviewSent(t *testing.T, a *Agent, id string) bool {
	t.Helper()
	var sent bool
	if err := a.store.db.QueryRow(`SELECT review_sent FROM inbox WHERE id = ?`, id).Scan(&sent); err != nil {
		t.Fatal(err)
	}
	return sent
}

// A held question on a machine nobody watches (bob) reaches the person
// through the agent they named (alice): one content-free notice per item,
// filed there for the person, never run, accepted or sent back, and
// deciding stays on bob.
func TestReviewNoticeReachesNamedAgentOnce(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	bobNotes, aliceNotes := fakeNotify(w.bob), fakeNotify(w.alice)
	bobNotes.fail = errors.New("no desktop here")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	stopBob, _ := runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})

	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "private greeting text", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "held question", func() bool { s, _ := w.bob.store.jobState(q.ID); return s == stateHeld })
	eventually(t, "notice at alice", func() bool { return len(notices(t, w.alice, w.bob.Address)) == 1 })
	n := notices(t, w.alice, w.bob.Address)[0]
	if n.State != stateNeedHuman || !strings.Contains(n.Detail, "review notice") {
		t.Fatalf("notice row = %+v", n)
	}
	if strings.Contains(n.Body, "private greeting") || strings.Contains(n.Body, q.ID) || strings.Contains(n.Body, w.alice.Address) {
		t.Fatalf("notice carries request details: %q", n.Body)
	}
	if !reviewSent(t, w.bob, q.ID) {
		t.Fatal("covered item not marked")
	}
	// The desktop notice on bob failed; the review notice did not replace or
	// suppress it: bob tried it. Alice shows no banner: a report from another
	// machine is not a decision here (owner decision 2026-09-30); it is
	// listed as a report, apart from her own review items.
	if bobNotes.count() == 0 {
		t.Fatal("bob's desktop notification was not attempted")
	}
	quiet(t, w.alice, aliceNotes, 0)
	if review, _ := w.alice.Review(); len(review) != 0 {
		t.Fatalf("a report is listed as alice's decision: %+v", review)
	}
	if reports, _ := w.alice.Notices(); len(reports) != 1 || reports[0].ID != n.ID {
		t.Fatalf("alice's reports = %+v", reports)
	}

	// Alice names bob too: her notice is received, not a local request, so
	// nothing goes back and no loop starts.
	if err := w.alice.SetReviewTo(tctx(t), w.bob.Address); err != nil {
		t.Fatal(err)
	}
	w.alice.wakeWorker()
	// Repeated wake-ups and a restart of bob report nothing new.
	for i := 0; i < 3; i++ {
		w.bob.wakeWorker()
	}
	stopBob()
	stopBob, _ = runWith(t, w, w.bob, RunOptions{})
	time.Sleep(500 * time.Millisecond)
	if got := notices(t, w.bob, w.alice.Address); len(got) != 0 {
		t.Fatalf("notice forwarded back: %+v", got)
	}
	if got := notices(t, w.alice, w.bob.Address); len(got) != 1 {
		t.Fatalf("notices after wake-ups and restart = %d", len(got))
	}

	// The notice never runs and cannot be accepted or declined; resolving
	// it changes nothing on bob.
	if st.count() != 0 {
		t.Fatalf("a worker ran %d time(s)", st.count())
	}
	if err := w.alice.Accept(n.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("accept notice: %v", err)
	}
	if _, err := w.alice.Decline(tctx(t), n.ID, "no"); err == nil {
		t.Fatal("declined a notice")
	}
	if err := w.alice.Resolve(n.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := w.bob.store.jobState(q.ID); s != stateHeld {
		t.Fatalf("bob's question after resolve: %s", s)
	}

	// A new item is reported with the current count.
	q2, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "second", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "second notice", func() bool { return len(notices(t, w.alice, w.bob.Address)) == 2 })
	// A device that may not decide gets how many wait and who decides
	// (nobody yet), never the requests (MEL-532).
	if r, ok := ParseReport(notices(t, w.alice, w.bob.Address)[1].Body); !ok || r.Count != 2 || len(r.Items) != 0 || len(r.Deciders) != 0 {
		t.Fatalf("second notice = %+v %v", r, ok)
	}
	if !reviewSent(t, w.bob, q2.ID) || st.count() != 0 {
		t.Fatal("second item not marked, or something ran")
	}
}

// A notice that cannot be queued leaves its items to be reported later.
func TestReviewNoticeNotQueuedStaysPending(t *testing.T) {
	w := newWorld(t, "")
	fakeNotify(w.bob)
	// An address the Hub does not list (set before it was checked, or
	// removed since): SetReviewTo refuses one now.
	if err := w.bob.store.setConfig(map[string]string{reviewToKey: "nobody/none", reviewToGenKey: protocol.NewID()}); err != nil {
		t.Fatal(err)
	}
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "held question", func() bool { s, _ := w.bob.store.jobState(q.ID); return s == stateHeld })
	time.Sleep(300 * time.Millisecond)
	if reviewSent(t, w.bob, q.ID) {
		t.Fatal("marked although no notice was queued")
	}
	var queued int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE status = ?`, envelope.StatusReviewNotice).Scan(&queued)
	if queued != 0 {
		t.Fatalf("%d notice(s) queued", queued)
	}

	// Wake-ups alone (as Hub pings cause) do not retry the failed item.
	for i := 0; i < 3; i++ {
		w.bob.wakeWorker()
	}
	time.Sleep(300 * time.Millisecond)
	if reviewSent(t, w.bob, q.ID) {
		t.Fatal("retried on a wake-up")
	}
	// Correcting the address wakes the running daemon and retries at once,
	// without a restart.
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	eventually(t, "notice after fixing the address", func() bool { return len(notices(t, w.alice, w.bob.Address)) == 1 })
	if !reviewSent(t, w.bob, q.ID) {
		t.Fatal("not marked after the notice was queued")
	}
}

func TestReviewToRefusesSelfAndBadAddresses(t *testing.T) {
	w := newWorld(t, "")
	for _, addr := range []string{w.bob.Address, "", "nobody", w.alice.Address + "#" + protocol.NewID()} {
		if err := w.bob.SetReviewTo(tctx(t), addr); err == nil {
			t.Fatalf("accepted %q", addr)
		}
	}
	if to, _ := w.bob.ReviewTo(); to != "" {
		t.Fatalf("set to %q", to)
	}
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.ClearReviewTo(); err != nil {
		t.Fatal(err)
	}
	if to, _ := w.bob.ReviewTo(); to != "" {
		t.Fatalf("still %q", to)
	}
}

// BUG-31: review-to refuses an address the Hub does not list as an agent
// (a typo, an agent that never joined) instead of reporting success, and
// one the Hub revoked; and doctor says when notices to the address set
// are not getting out, instead of "ok" while only the daemon log knows.
func TestReviewToChecksTheHubAndDoctorShowsFailures(t *testing.T) {
	w := newWorld(t, "")
	fakeNotify(w.bob)
	for _, addr := range []string{"ghost/nowhere", "admin/alicee"} {
		err := w.bob.SetReviewTo(tctx(t), addr)
		if err == nil || !strings.Contains(err.Error(), "not an agent on your Hub") {
			t.Fatalf("review-to %s: %v", addr, err)
		}
	}
	carol := mustJoin(t, t.TempDir(), w.aliceInvites("carol"), "desk")
	if err := w.alice.Revoke(tctx(t), carol.Address); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.SetReviewTo(tctx(t), carol.Address); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("review-to a revoked agent: %v", err)
	}
	if to, _ := w.bob.ReviewTo(); to != "" {
		t.Fatalf("set to %q", to)
	}
	doctor := func() (Check, bool) {
		for _, c := range w.bob.Doctor(tctx(t)) {
			if c.Name == "review-to" {
				return c, true
			}
		}
		return Check{}, false
	}
	if c, ok := doctor(); ok {
		t.Fatalf("review-to off, yet doctor says %+v", c)
	}
	// An address that stopped being listed after it was set: the notice
	// cannot be queued, and doctor says so (not only the daemon log).
	if err := w.bob.store.setConfig(map[string]string{reviewToKey: "nobody/none", reviewToGenKey: protocol.NewID()}); err != nil {
		t.Fatal(err)
	}
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "held question", func() bool { s, _ := w.bob.store.jobState(q.ID); return s == stateHeld })
	eventually(t, "doctor to say notices fail", func() bool {
		c, ok := doctor()
		return ok && !c.OK && strings.Contains(c.Result, "review notices to nobody/none are not getting out") && strings.Contains(c.Result, "could not be queued")
	})
	// Fixed: the notice goes out and doctor is content again.
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	eventually(t, "notice after fixing the address", func() bool { return len(notices(t, w.alice, w.bob.Address)) == 1 })
	if c, ok := doctor(); !ok || !c.OK || c.Result != "notices go to "+w.alice.Address+"; none failed" {
		t.Fatalf("doctor after the fix: %+v %v", c, ok)
	}
}

// A notice the Hub refused once does not keep doctor failing after later
// notices to the same address got through: only refusals since the last
// one that went out count (review finding 4).
func TestReviewToHealthForgetsRefusalsBeforeADelivery(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	notice := func(state, why string, ms int64) {
		t.Helper()
		if _, err := w.bob.store.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, error, created_at, status, created_ms) VALUES(?, ?, 'n', '{}', ?, nullif(?, ''), ?, ?, ?)`,
			protocol.NewID(), w.alice.Address, state, why, ms/1000, envelope.StatusReviewNotice, ms); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	notice(stateFailed, "refused: unknown recipient", now-3000)
	notice(protocol.StateDelivered, "", now-2000)
	if h, err := w.bob.ReviewToHealth(); err != nil || h != "" {
		t.Fatalf("a refusal before a delivered notice still counts: %q %v", h, err)
	}
	notice(stateFailed, "refused again", now-1000)
	if h, err := w.bob.ReviewToHealth(); err != nil || !strings.Contains(h, "the Hub refused 1 notice(s) (last: refused again)") {
		t.Fatalf("a refusal after the last delivered notice: %q %v", h, err)
	}
}

// Only the exact notice shape is filed for the person; everything else,
// including ordinary replies, stays quiet. A duplicate delivery is stored
// once.
func TestReviewNoticeShape(t *testing.T) {
	w := newWorld(t, "")
	base := envelope.Inner{From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Body: "x"}
	cases := []struct {
		name string
		mod  func(*envelope.Inner)
		want string
	}{
		{"notice", func(in *envelope.Inner) { in.Kind, in.Status = envelope.KindMessage, envelope.StatusReviewNotice }, stateNeedHuman},
		{"plain message", func(in *envelope.Inner) { in.Kind = envelope.KindMessage }, ""},
		{"message with done", func(in *envelope.Inner) { in.Kind, in.Status = envelope.KindMessage, envelope.StatusDone }, ""},
		{"answer with notice status", func(in *envelope.Inner) { in.Kind, in.Status = envelope.KindAnswer, envelope.StatusReviewNotice }, ""},
		{"notice as a reply", func(in *envelope.Inner) {
			in.Kind, in.Status, in.ReplyTo = envelope.KindMessage, envelope.StatusReviewNotice, protocol.NewID()
		}, ""},
		{"notice with a file", func(in *envelope.Inner) {
			in.Kind, in.Status = envelope.KindMessage, envelope.StatusReviewNotice
			in.Attachments = []envelope.Attachment{{Name: "f"}}
		}, ""},
	}
	for _, c := range cases {
		in := base
		in.ID = protocol.NewID()
		c.mod(&in)
		if got, err := initialState(w.bob.store.db, in, ""); err != nil || got != c.want {
			t.Errorf("%s: state %q (%v), want %q", c.name, got, err, c.want)
		}
	}

	in := base
	in.ID, in.Kind, in.Status = protocol.NewID(), envelope.KindMessage, envelope.StatusReviewNotice
	for i := 0; i < 2; i++ {
		if err := w.bob.store.addInbox(in, ""); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	var detail sql.NullString
	w.bob.store.db.QueryRow(`SELECT count(*), max(detail) FROM inbox WHERE id = ?`, in.ID).Scan(&n, &detail)
	if n != 1 || !strings.Contains(detail.String, w.alice.Address) {
		t.Fatalf("rows %d, detail %q", n, detail.String)
	}
}

// An item reported once and then back in review for a new reason (accepted,
// then the responder asks for a person) is reported again in the same run.
func TestReviewNoticeAgainAfterNeedsHuman(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	fakeNotify(w.bob)
	setResponder(t, w.bob, "stubhuman", st.dir, time.Minute)
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "q", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "first notice", func() bool { return len(notices(t, w.alice, w.bob.Address)) == 1 })
	if err := w.bob.Accept(q.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "needs_human", func() bool { s, _ := w.bob.store.jobState(q.ID); return s == stateNeedHuman })
	// Reported again: a newer snapshot names it waiting (one in between may
	// have settled the card while it ran: MEL-532).
	eventually(t, "second notice", func() bool {
		ns := notices(t, w.alice, w.bob.Address)
		open := mustNotices(t, w.alice)
		if len(ns) < 2 || len(open) != 1 || open[0].ID == ns[0].ID {
			return false
		}
		r, ok := ParseReport(open[0].Body)
		return ok && r.Count == 1
	})
}

// Follow-ups the local responder marked needs_human (answers, results,
// messages) are local items and count; received notices do not, and
// anything short of the exact notice shape is not treated as one.
func TestReviewNoticeCountsLocalFollowUps(t *testing.T) {
	w := newWorld(t, "")
	if err := w.bob.SetReviewTo(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	add := func(kind, status, replyTo string) string {
		id := protocol.NewID()
		if _, err := w.bob.store.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, reply_to, received_at, status, state)
			VALUES(?, ?, 0, ?, 'x', nullif(?, ''), 0, nullif(?, ''), ?)`, id, w.alice.Address, kind, replyTo, status, stateNeedHuman); err != nil {
			t.Fatal(err)
		}
		return id
	}
	local := []string{
		add(envelope.KindAnswer, envelope.StatusDone, protocol.NewID()),
		add(envelope.KindResult, envelope.StatusFailed, protocol.NewID()),
		add(envelope.KindMessage, "", protocol.NewID()),
		add(envelope.KindMessage, envelope.StatusReviewNotice, protocol.NewID()), // not the exact shape
	}
	received := add(envelope.KindMessage, envelope.StatusReviewNotice, "")
	w.bob.sendReviewNotice(tctx(t))
	for _, id := range local {
		if !reviewSent(t, w.bob, id) {
			t.Fatalf("local item %s not reported", id)
		}
	}
	if reviewSent(t, w.bob, received) {
		t.Fatal("received notice reported onward")
	}
	var body string
	if err := w.bob.store.db.QueryRow(`SELECT body FROM outbox WHERE status = ?`, envelope.StatusReviewNotice).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(body, "4 request(s) wait") {
		t.Fatalf("notice = %q", body)
	}
}
