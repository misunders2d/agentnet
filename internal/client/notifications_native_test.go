package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNativeReviewAndReminderExactDestinations(t *testing.T) {
	w := newWorld(t, "")
	n := fakeNotify(w.bob)
	var got []string
	w.bob.nativeNotify = func(fragment string) { got = append(got, fragment) }
	id := protocol.NewID()
	_, e := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, w.alice.Address, 1, envelope.KindQuestion, "PRIVATE content", 1, stateHeld, w.alice.Self().Fingerprint())
	if e != nil {
		t.Fatal(e)
	}
	w.bob.notifyReview()
	w.bob.notifyReview()
	if len(got) != 1 || got[0] != "msg="+id+"&dir=in" {
		t.Fatalf("review routes %v", got)
	}
	if _, e = w.bob.SetReminder(id, time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if _, e = w.bob.remindDue(time.Now().Add(2 * time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || got[1] != "msg="+id+"&dir=in" {
		t.Fatalf("reminder routes %v", got)
	}
	if _, e = w.bob.remindDue(time.Now().Add(2 * time.Hour)); e != nil {
		t.Fatal(e)
	}
	if len(got) != 2 || n.count() != 0 {
		t.Fatal("repeat or desktop notification")
	}
	notice := insertReviewAlert(t, w.bob, w.alice.Address, w.alice.Self().Fingerprint(), reviewAlertBody(t, w.alice.Address, reviewAlertItem(w.alice)))
	if route := w.bob.nativeReviewFragment(notice); route != "review" {
		t.Fatal("received report did not open actionable activity", route)
	}
}
func TestHumanOnlyReviewAttentionUsesChanges(t *testing.T) {
	w := newWorld(t, "")
	var mu sync.Mutex
	var got []string
	stop, _ := runWith(t, w, w.bob, RunOptions{HumanOnly: true, Notify: func(fragment string) { mu.Lock(); got = append(got, fragment); mu.Unlock() }})
	id := protocol.NewID()
	_, e := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, w.alice.Address, 1, envelope.KindQuestion, "PRIVATE", 1, stateHeld, w.alice.Self().Fingerprint())
	if e != nil {
		t.Fatal(e)
	}
	w.bob.NoteChange()
	eventually(t, "native review change", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) == 1 })
	stop()
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "msg="+id+"&dir=in" {
		t.Fatal(got)
	}
}

func TestWorkerStartupReviewUsesRunNativeNotifier(t *testing.T) {
	t.Run("authorized-before-held-review", testWorkerDispatchBeforeHeldReview)
	w := newWorld(t, "")
	desktop := fakeNotify(w.bob)
	id := protocol.NewID()
	if _, err := w.bob.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,state,verified_by) VALUES(?,?,?,?,?,?,?,?)`, id, w.alice.Address, 1, envelope.KindQuestion, "PRIVATE", 1, stateHeld, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []string
	stop, _ := runWith(t, w, w.bob, RunOptions{Notify: func(fragment string) {
		mu.Lock()
		got = append(got, fragment)
		mu.Unlock()
	}})
	eventually(t, "startup held review uses native notifier", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(got) == 1
	})
	stop()
	mu.Lock()
	defer mu.Unlock()
	if got[0] != "msg="+id+"&dir=in" || desktop.count() != 0 {
		t.Fatalf("startup review routes=%v desktop=%d", got, desktop.count())
	}
}

// A slow unrelated decision notification must not precede an authorized claim.
func testWorkerDispatchBeforeHeldReview(t *testing.T) {
	dir := concurrencyHarness(t)
	w := newWorld(t, "")
	setResponder(t, w.bob, "concurrencybarrier", dir, time.Minute)
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	pending := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindTask, Body: "unapproved unrelated decision"})
	ready := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: envelope.KindQuestion, Body: "already authorized work"})
	for _, env := range []envelope.Envelope{pending, ready} {
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
	if got := jobState(t, w.bob, pending.ID); got != stateAwaiting {
		t.Fatalf("task bypassed consent before worker startup: %s", got)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce, notifyOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	var fragment string
	w.bob.nativeNotify = func(route string) {
		notifyOnce.Do(func() {
			fragment = route
			close(entered)
			<-release
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopWorker, err := w.bob.startWorker(ctx)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer func() { unblock(); cancel(); stopWorker() }()
	select {
	case <-entered:
	case <-tctx(t).Done():
		t.Fatal("pending decision never reached native notification")
	}
	// The actual synthetic subprocess must start while notification is held.
	requireBarrier(t, ready.ID)
	if got := jobState(t, w.bob, ready.ID); got != stateRunning {
		t.Fatalf("authorized work did not dispatch before review returned: %s", got)
	}
	if barrierStarted(pending.ID) || jobState(t, w.bob, pending.ID) != stateAwaiting {
		t.Fatal("unapproved task ran while its notification was held")
	}
	releaseBarrier(t, ready.ID)
	waitState(t, w.bob, ready.ID, stateAnswered)
	var replies int
	if err = w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE reply_to=?`, ready.ID).Scan(&replies); err != nil || replies != 1 {
		t.Fatalf("authorized request lost exact final answer: replies=%d err=%v", replies, err)
	}
	var replyID string
	if err = w.bob.store.db.QueryRow(`SELECT result_id FROM inbox WHERE id=?`, ready.ID).Scan(&replyID); err != nil {
		t.Fatal(err)
	}
	reply, err := w.bob.store.outboxEnvelope(replyID)
	if err != nil {
		t.Fatal(err)
	}
	answer, err := envelope.Open(reply, w.alice.id, w.alice.Address, w.bob.Self())
	if err != nil || answer.Kind != envelope.KindAnswer || answer.Status != envelope.StatusDone || answer.Body != "barrier answer" || answer.ReplyTo != ready.ID || answer.From != w.bob.Address || answer.To != w.alice.Address {
		t.Fatalf("authorized request's signed answer changed: %+v err=%v", answer, err)
	}
	if fragment != "msg="+pending.ID+"&dir=in" {
		t.Fatalf("pending decision notification route changed: %s", fragment)
	}
	unblock()
	eventually(t, "pending task notification recorded", func() bool {
		var notified bool
		return w.bob.store.db.QueryRow(`SELECT notified FROM inbox WHERE id=?`, pending.ID).Scan(&notified) == nil && notified
	})
	var state string
	var attempts int
	if err = w.bob.store.db.QueryRow(`SELECT state,attempts FROM inbox WHERE id=?`, pending.ID).Scan(&state, &attempts); err != nil || state != stateAwaiting || attempts != 0 || barrierStarted(pending.ID) {
		t.Fatalf("review changed unapproved task authority: state=%s attempts=%d err=%v", state, attempts, err)
	}
}
