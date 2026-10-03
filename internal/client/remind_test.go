package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// remindWorld: alice and bob; bob's daemon runs with its notifications
// recorded and clicks that open the thread or the page. The thread's click
// is built as on Windows (no terminal launcher to look up) on every OS,
// macOS included, whose notifier takes no clicks; nothing is started.
func remindWorld(t *testing.T) (w *world, n *notes, stopBob func()) {
	t.Helper()
	oldOS, oldStart := clickOS, startConsole
	clickOS, startConsole = "windows", func([]string, string) error { return nil }
	t.Cleanup(func() { clickOS, startConsole = oldOS, oldStart })
	w = newWorld(t, "")
	n = fakeNotify(w.bob)
	runAgent(t, w.alice)
	stopBob, _ = runWith(t, w, w.bob, RunOptions{OpenConv: func(conv string) []string { return []string{"open-page", conv} }})
	return w, n, stopBob
}

func receiveAt(t *testing.T, w *world, kind, body string) string {
	t.Helper()
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: body, Kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold "+sent.ID, func() bool { return inboxCount(t, w.bob, `id = ?`, sent.ID) == 1 })
	return sent.ID
}

// reminded counts the reminder notifications (a held question also gets
// its own review notification).
func (n *notes) reminded() (count int, bodies []string, argvs [][]string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for i, b := range n.bodies {
		if strings.Contains(b, "reminder") {
			count++
			bodies, argvs = append(bodies, b), append(argvs, n.argvs[i])
		}
	}
	return count, bodies, argvs
}

func reminderOf(t *testing.T, a *Agent, id string) Reminder {
	t.Helper()
	r, ok, err := a.Reminder(id)
	if err != nil || !ok {
		t.Fatalf("reminder on %s: %v %v", id, ok, err)
	}
	return r
}

// At its time a reminder asks for attention once, content-free, with a
// click on the message's thread; it stays overdue; the message is
// untouched; moving it is a new revision, notified once more.
func TestReminderDueOnceAndMoved(t *testing.T) {
	w, n, _ := remindWorld(t)
	q := receiveAt(t, w, envelope.KindQuestion, "budget for Q4?")
	before, _ := w.bob.store.jobState(q)
	if _, err := w.bob.SetReminder(q, time.Now().Add(-time.Minute)); err == nil {
		t.Fatal("a reminder in the past")
	}
	r, err := w.bob.SetReminder(q, time.Now().Add(1500*time.Millisecond))
	if err != nil || r.State != ReminderPending || r.Overdue || r.Alerted || r.From != w.alice.Address {
		t.Fatalf("set: %+v %v", r, err)
	}
	time.Sleep(500 * time.Millisecond)
	if c, _, _ := n.reminded(); c != 0 {
		t.Fatal("notified early")
	}
	eventually(t, "the reminder", func() bool { c, _, _ := n.reminded(); return c == 1 })
	_, bodies, argvs := n.reminded()
	body, argv := bodies[0], argvs[0]
	if body != "AgentNet: A reminder is due" || len(argv) == 0 || argv[len(argv)-1] != q || strings.Contains(strings.Join(argv, " "), "budget") {
		t.Fatalf("notification %q, click %v", body, argv)
	}
	if r = reminderOf(t, w.bob, q); !r.Overdue || !r.Alerted || r.State != ReminderPending {
		t.Fatalf("after its time: %+v", r)
	}
	time.Sleep(1200 * time.Millisecond)
	if c, _, _ := n.reminded(); c != 1 {
		t.Fatalf("notified %d times for one revision", c)
	}
	var sent int
	w.bob.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE ref_id IS NULL`).Scan(&sent) // execution statuses to the requester are not the reminder's doing
	if after, _ := w.bob.store.jobState(q); after != before || sent != 0 {
		t.Fatalf("the reminder changed the message (%s -> %s) or sent something", before, after)
	}
	if _, err := w.bob.SetReminder(q, time.Now().Add(1200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if r = reminderOf(t, w.bob, q); r.Overdue || r.Alerted {
		t.Fatalf("moved: %+v", r)
	}
	eventually(t, "the moved reminder", func() bool { c, _, _ := n.reminded(); return c == 2 })
}

// Cancelled or done, a reminder never asks for attention.
func TestReminderCancelAndDone(t *testing.T) {
	w, n, stopBob := remindWorld(t)
	a, b := receiveAt(t, w, envelope.KindMessage, "one"), receiveAt(t, w, envelope.KindMessage, "two")
	stopBob()
	// End both reminders before advancing the scheduler past their due time.
	// A live one-second deadline could elapse during setup on a busy runner,
	// legitimately notifying before CancelReminder or DoneReminder ran.
	due := time.Now().Add(time.Hour)
	for _, id := range []string{a, b} {
		if _, err := w.bob.SetReminder(id, due); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.bob.CancelReminder(a); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.DoneReminder(b); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.DoneReminder(b); err != ErrNoReminder {
		t.Fatalf("done twice: %v", err)
	}
	if next, err := w.bob.remindDue(due.Add(time.Second)); err != nil || !next.IsZero() {
		t.Fatalf("ended reminders scheduled: %v, %v", next, err)
	}
	if n.count() != 0 {
		t.Fatal("an ended reminder notified")
	}
	for id, state := range map[string]string{a: ReminderCancelled, b: ReminderDone} {
		if r := reminderOf(t, w.bob, id); r.State != state || r.Alerted {
			t.Fatalf("ended reminder: %+v, want %s and unalerted", r, state)
		}
	}
	if rs, _ := w.bob.Reminders(false); len(rs) != 0 {
		t.Fatalf("pending: %+v", rs)
	}
	if rs, _ := w.bob.Reminders(true); len(rs) != 2 {
		t.Fatalf("all: %+v", rs)
	}
}

// Only a reply to that very message ends its reminder, by any reply path:
// a reply by hand, a decline, the worker's answer, a DM reply. A message
// that is not a reply to it, the worker's failure report, reading it do
// not.
func TestReminderEndsOnlyWithAReply(t *testing.T) {
	st := installStub(t, "answer")
	w, _, _ := remindWorld(t)
	set := func(id string) {
		t.Helper()
		if _, err := w.bob.SetReminder(id, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	stateOf := func(id string) string { return reminderOf(t, w.bob, id).State }

	byHand, declined, other := receiveAt(t, w, envelope.KindQuestion, "by hand"), receiveAt(t, w, envelope.KindTask, "declined"), receiveAt(t, w, envelope.KindMessage, "other")
	progressTarget := receiveAt(t, w, envelope.KindQuestion, "progress")
	for _, id := range []string{byHand, declined, other, progressTarget} {
		set(id)
	}
	w.bob.Inbox(false, true) // reading
	if _, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "not a reply"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.Reply(tctx(t), byHand, "answered"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.Decline(tctx(t), declined, "no"); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.store.addInbox(envelope.Inner{ID: "progress-reply", From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "still working", ReplyTo: progressTarget, Status: envelope.StatusProgress}, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if stateOf(byHand) != ReminderReplied || stateOf(declined) != ReminderReplied || stateOf(other) != ReminderPending || stateOf(progressTarget) != ReminderPending {
		t.Fatalf("by hand %s, declined %s, other %s, progress %s", stateOf(byHand), stateOf(declined), stateOf(other), stateOf(progressTarget))
	}

	// The worker: its answer ends it; its failure report does not. Each is
	// held, its reminder set, then accepted, so the worker replies after.
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	for _, c := range []struct {
		mode, jobState, reminder string
	}{{"fail", stateJobFailed, ReminderPending}, {"answer", stateAnswered, ReminderReplied}} {
		t.Setenv("STUB_MODE", c.mode)
		q := receiveAt(t, w, envelope.KindQuestion, "for the worker: "+c.mode)
		set(q)
		if err := w.bob.Accept(q); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the worker's "+c.mode, func() bool { s, _ := w.bob.store.jobState(q); return s == c.jobState })
		if got := stateOf(q); got != c.reminder {
			t.Fatalf("worker %s: reminder %s, want %s", c.mode, got, c.reminder)
		}
	}

	// A DM: a reply naming the message ends it; another DM message does not.
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	dmq, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "dm question"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold the DM question", func() bool { return inboxCount(t, w.bob, `id = ?`, dmq.ID) == 1 })
	set(dmq.ID)
	if r := reminderOf(t, w.bob, dmq.ID); r.Conv != conv {
		t.Fatalf("DM reminder: %+v", r)
	}
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if stateOf(dmq.ID) != ReminderPending {
		t.Fatal("an unlinked DM message ended the reminder")
	}
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "here you go", ReplyTo: dmq.ID}); err != nil {
		t.Fatal(err)
	}
	if stateOf(dmq.ID) != ReminderReplied {
		t.Fatalf("a DM reply did not end it: %s", stateOf(dmq.ID))
	}
}

// Nothing comes due while the daemon is stopped: a reminder due then is
// overdue at the next start and notified once, not again after another
// restart. A DM reminder's click opens the DM on the page.
func TestReminderOverdueAtStart(t *testing.T) {
	w, n, stopBob := remindWorld(t)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "later"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold it", func() bool { return inboxCount(t, w.bob, `id = ?`, sent.ID) == 1 })
	if _, err := w.bob.SetReminder(sent.ID, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stopBob()
	time.Sleep(1500 * time.Millisecond)
	if n.count() != 0 {
		t.Fatal("notified while stopped")
	}
	stopBob, _ = runWith(t, w, w.bob, RunOptions{OpenConv: func(conv string) []string { return []string{"open-page", conv} }})
	eventually(t, "the overdue reminder", func() bool { return n.count() == 1 })
	n.mu.Lock()
	argv := n.argvs[0]
	n.mu.Unlock()
	if len(argv) != 2 || argv[1] != conv {
		t.Fatalf("click %v", argv)
	}
	stopBob()
	runAgent(t, w.bob)
	time.Sleep(time.Second)
	if n.count() != 1 || !reminderOf(t, w.bob, sent.ID).Overdue {
		t.Fatalf("after another restart: %d notifications, %+v", n.count(), reminderOf(t, w.bob, sent.ID))
	}
}

// A reminder is for the next ten years at most (the far future is refused
// before it is stored); one years ahead is stored and not due.
func TestReminderRange(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	id := receiveAt(t, w, envelope.KindMessage, "someday")
	for _, far := range []time.Time{time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().Add(maxReminderAhead + time.Hour)} {
		if _, err := w.bob.SetReminder(id, far); err == nil {
			t.Fatalf("a reminder at %s", far)
		}
	}
	if _, ok, _ := w.bob.Reminder(id); ok {
		t.Fatal("a refused reminder was stored")
	}
	r, err := w.bob.SetReminder(id, time.Now().AddDate(9, 0, 0))
	if err != nil || r.Overdue || r.Alerted {
		t.Fatalf("nine years ahead: %+v %v", r, err)
	}
}

// Only a received message takes a reminder.
func TestReminderOnlyOnReceivedMessages(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	sent, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{sent.ID, strings.Repeat("0", 32), "nope"} {
		if _, err := w.bob.SetReminder(id, time.Now().Add(time.Hour)); err == nil {
			t.Errorf("a reminder on %q", id)
		}
	}
}

// A message its sender deleted takes no reminder: there is nothing left
// to come back to.
func TestReminderRefusedOnDeletedMessage(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	id := receiveAt(t, w, envelope.KindMessage, "never mind")
	ref, err := w.alice.RefOf("", id, "out")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the deletion sent", func() bool {
		_, err := w.alice.Retract(tctx(t), ref, "")
		return err == nil
	})
	eventually(t, "bob to hold the deletion", func() bool {
		gone, err := w.bob.store.retracted(id)
		return err == nil && gone
	})
	if _, err := w.bob.SetReminder(id, time.Now().Add(time.Hour)); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("a reminder on a deleted message: %v", err)
	}
	if _, ok, _ := w.bob.Reminder(id); ok {
		t.Fatal("a refused reminder was stored")
	}
}
