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
	if err := w.bob.SetReviewTo(w.alice.Address); err != nil {
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
	// suppress it: bob tried it, and alice showed her own count-only banner.
	if bobNotes.count() == 0 {
		t.Fatal("bob's desktop notification was not attempted")
	}
	eventually(t, "alice's banner", func() bool { return aliceNotes.count() > 0 })
	if strings.Contains(aliceNotes.last(), "private greeting") {
		t.Fatalf("banner = %q", aliceNotes.last())
	}

	// Alice names bob too: her notice is received, not a local request, so
	// nothing goes back and no loop starts.
	if err := w.alice.SetReviewTo(w.bob.Address); err != nil {
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
	if b := notices(t, w.alice, w.bob.Address)[1].Body; !strings.HasPrefix(b, "2 request(s) wait") {
		t.Fatalf("second notice = %q", b)
	}
	if !reviewSent(t, w.bob, q2.ID) || st.count() != 0 {
		t.Fatal("second item not marked, or something ran")
	}
}

// A notice that cannot be queued leaves its items to be reported later.
func TestReviewNoticeNotQueuedStaysPending(t *testing.T) {
	w := newWorld(t, "")
	fakeNotify(w.bob)
	if err := w.bob.SetReviewTo("nobody/none"); err != nil {
		t.Fatal(err)
	}
	stopBob, _ := runWith(t, w, w.bob, RunOptions{})
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

	stopBob()
	if err := w.bob.SetReviewTo(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runWith(t, w, w.bob, RunOptions{})
	eventually(t, "notice after fixing the address", func() bool { return len(notices(t, w.alice, w.bob.Address)) == 1 })
	if !reviewSent(t, w.bob, q.ID) {
		t.Fatal("not marked after the notice was queued")
	}
}

func TestReviewToRefusesSelfAndBadAddresses(t *testing.T) {
	w := newWorld(t, "")
	for _, addr := range []string{w.bob.Address, "", "nobody", w.alice.Address + "#" + protocol.NewID()} {
		if err := w.bob.SetReviewTo(addr); err == nil {
			t.Fatalf("accepted %q", addr)
		}
	}
	if to, _ := w.bob.ReviewTo(); to != "" {
		t.Fatalf("set to %q", to)
	}
	if err := w.bob.SetReviewTo(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.ClearReviewTo(); err != nil {
		t.Fatal(err)
	}
	if to, _ := w.bob.ReviewTo(); to != "" {
		t.Fatalf("still %q", to)
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
		if got, err := initialState(w.bob.store.db, in); err != nil || got != c.want {
			t.Errorf("%s: state %q (%v), want %q", c.name, got, err, c.want)
		}
	}

	in := base
	in.ID, in.Kind, in.Status = protocol.NewID(), envelope.KindMessage, envelope.StatusReviewNotice
	for i := 0; i < 2; i++ {
		if err := w.bob.store.addInbox(in); err != nil {
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
