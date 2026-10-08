package client

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
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
	due := time.Now().Add(1500 * time.Millisecond)
	r, err := w.bob.SetReminder(q, due)
	if err != nil || r.State != ReminderPending || r.Overdue || r.Alerted || r.From != w.alice.Address {
		t.Fatalf("set: %+v %v", r, err)
	}
	// Not before its time: due times are kept in whole seconds, so the
	// earliest it may ask is the start of its due second. The count is read
	// before the clock, so a slow machine that oversleeps sees a reminder
	// that came on time, never a false "early".
	time.Sleep(500 * time.Millisecond)
	c, _, _ := n.reminded()
	if now := time.Now(); c != 0 && now.Before(time.Unix(due.Unix(), 0)) {
		t.Fatalf("notified early: %d by %s, due %s", c, now.Format(time.StampMilli), due.Format(time.StampMilli))
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

// Replies can name the logical turn or a stored physical copy. A logical
// id in another conversation and non-answer statuses must not end it.
func TestReminderReplyMatchesLogicalOrPhysicalID(t *testing.T) {
	s, err := openStore(filepath.Join(t.TempDir(), "agent.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	lid := protocol.NewID()
	ids := []string{protocol.NewID(), protocol.NewID(), protocol.NewID(), protocol.NewID()}
	for i, conv := range []string{"conversation", "conversation", "other", ""} {
		if _, err := s.db.Exec(`INSERT INTO inbox(id, sender, ts, kind, body, received_at, conv, lid)
			VALUES(?, 'sender/desk', 1, 'message', 'remind me', 1, nullif(?, ''), ?)`, ids[i], conv, lid); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO reminders(message, conv, due_at, state, rev, created_at, updated_at)
			VALUES(?, nullif(?, ''), 2, 'pending', 1, 1, 1)`, ids[i], conv); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name, conv, reply, status string
		replied                   []int
	}{
		{"physical", "conversation", ids[0], "", []int{0}},
		{"logical", "conversation", lid, "", []int{0, 1}},
		{"other conversation", "other", lid, "", []int{2}},
		{"wrong conversation", "other", ids[0], "", nil},
		{"unlinked", "conversation", "", "", nil},
		{"progress", "conversation", lid, envelope.StatusProgress, nil},
		{"failed", "conversation", lid, envelope.StatusFailed, nil},
		{"timeout", "conversation", lid, envelope.StatusTimeout, nil},
		{"cancelled", "conversation", lid, envelope.StatusCancelled, nil},
		{"legacy physical", "", ids[3], "", []int{3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.db.Exec(`UPDATE reminders SET state = 'pending'`); err != nil {
				t.Fatal(err)
			}
			tx, err := s.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := replyEndsReminder(tx, c.conv, c.reply, c.status); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for i, id := range ids {
				want := ReminderPending
				for _, replied := range c.replied {
					if i == replied {
						want = ReminderReplied
					}
				}
				var state string
				if err := s.db.QueryRow(`SELECT state FROM reminders WHERE message = ?`, id).Scan(&state); err != nil || state != want {
					t.Fatalf("copy %d: %s, want %s: %v", i, state, want, err)
				}
			}
		})
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
	stopBob()
	if _, err := w.bob.SetReminder(sent.ID, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
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

// A locally stored sent message takes the same personal reminder.
func TestReminderOnOwnSentMessages(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	sent, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(time.Hour).Truncate(time.Second)
	r, err := w.bob.SetReminder(sent.ID, due)
	if err != nil || r.Message != sent.ID || r.From != w.bob.Address || r.Due != due.Unix() || r.State != ReminderPending {
		t.Fatalf("own reminder: %+v %v", r, err)
	}
	again, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	stored, ok, err := again.Reminder(sent.ID)
	if err != nil || !ok || stored != r {
		t.Fatalf("reopened reminder: %+v %v %v", stored, ok, err)
	}
	ref, err := w.bob.RefOf("", sent.ID, "out")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.Retract(tctx(t), ref, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.SetReminder(sent.ID, due); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("deleted own reminder: %v", err)
	}
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	turn, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "own linked-person turn"})
	if err != nil {
		t.Fatal(err)
	}
	for _, copy := range turn.Copies {
		r, err := w.bob.SetReminder(copy.ID, due)
		if err != nil || r.Message != copy.ID || r.Conv != conv || r.From != w.bob.Address {
			t.Fatalf("own DM reminder: %+v %v", r, err)
		}
	}
	for _, id := range []string{strings.Repeat("0", 32), "nope"} {
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

// Fanout copies keep their exact local reminder IDs; a logical reply ends
// matching copies only within its signed conversation, as for incoming turns.
func TestReminderOwnFanoutCopies(t *testing.T) {
	w := newWorld(t, "")
	lid := protocol.NewID()
	ids := []string{protocol.NewID(), protocol.NewID(), protocol.NewID()}
	due := time.Now().Add(time.Hour).Truncate(time.Second)
	for i, conv := range []string{"group", "group", "other"} {
		if _, err := w.bob.store.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind) VALUES(?, 'peer/device', 'own turn', '{}', 'delivered', 1, ?, ?, 'message')`, ids[i], conv, lid); err != nil {
			t.Fatal(err)
		}
		r, err := w.bob.SetReminder(ids[i], due)
		if err != nil || r.Message != ids[i] || r.Conv != conv || r.From != w.bob.Address {
			t.Fatalf("copy %d: %+v %v", i, r, err)
		}
	}
	again, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	for _, id := range ids {
		r, ok, err := again.Reminder(id)
		if err != nil || !ok || r.Message != id || r.Due != due.Unix() {
			t.Fatalf("reopen %s: %+v %v %v", id, r, ok, err)
		}
	}
	tx, err := w.bob.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := replyEndsReminder(tx, "group", lid, ""); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		r, _, err := w.bob.Reminder(id)
		want := ReminderReplied
		if i == 2 {
			want = ReminderPending
		}
		if err != nil || r.State != want {
			t.Fatalf("reply copy %d: %+v %v", i, r, err)
		}
	}
}
