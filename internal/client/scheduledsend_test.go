package client

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func scheduledWorld(t *testing.T) (*world, string) {
	t.Helper()
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	stop()
	return w, conv
}
func futureSchedule(conv string) ScheduledSendInput {
	return ScheduledSendInput{Conv: conv, Body: "scheduled ordinary message", Due: time.Now().Add(time.Hour), Timezone: "Europe/Kyiv"}
}
func scheduleCopies(t *testing.T, a *Agent, s ScheduledSend) int {
	t.Helper()
	var n int
	if e := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE conv=? AND lid=?`, s.Conv, s.ID).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}
func scheduleOf(t *testing.T, a *Agent, id string) ScheduledSend {
	t.Helper()
	s, ok, e := a.ScheduledSend(id)
	if e != nil || !ok {
		t.Fatalf("schedule %v %v", ok, e)
	}
	return s
}

func TestScheduledSendReservesQueuedID(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	s, err := a.ScheduleSend(futureSchedule(conv))
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ordinary", "other-hook", "wrong-dispatch-id"} {
		ctx := WithQueuedSend(tctx(t), s.ID)
		out := ConvOutgoing{Body: "unrelated ordinary body"}
		if mode == "other-hook" {
			out.beforeStore = func(*sql.Tx, []outCopy, envelope.Inner) error { return nil }
		}
		// A dispatch marker is specific to the reserved ID, not a blanket exemption.
		if mode == "wrong-dispatch-id" {
			ctx = context.WithValue(ctx, scheduledDispatchKey{}, protocol.NewID())
		}
		if _, err = a.SendConv(ctx, conv, out); err == nil {
			t.Fatal(mode, "ordinary send used a reserved schedule ID")
		}
		var local int
		if err = a.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND lid=? AND local=1`, conv, s.ID).Scan(&local); err != nil {
			t.Fatal(err)
		}
		if local != 0 || scheduleCopies(t, a, s) != 0 || scheduleOf(t, a, s.ID).State != ScheduledPending {
			t.Fatal(mode, "reservation refusal changed durable state")
		}
	}
	ordinaryID := protocol.NewID()
	if _, err = a.SendConv(WithQueuedSend(tctx(t), ordinaryID), conv, ConvOutgoing{Body: "ordinary unrelated ID"}); err != nil {
		t.Fatal("unrelated ordinary send", err)
	}
	sent, err := a.SendScheduledNow(tctx(t), s.ID, s.Rev)
	if err != nil || sent.State != ScheduledSent || scheduleCopies(t, a, s) != 1 {
		t.Fatal("scheduler dispatch", sent, err)
	}
	var body, sealed string
	if err = a.store.db.QueryRow(`SELECT body,envelope FROM outbox WHERE conv=? AND lid=?`, conv, s.ID).Scan(&body, &sealed); err != nil || body != s.Body {
		t.Fatal("scheduler sent another body", body, err)
	}
	if _, err = a.store.db.Exec(`UPDATE scheduled_sends SET state='dispatching' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	a.stopBackgroundPosts()
	if err = a.Close(); err != nil {
		t.Fatal(err)
	}
	a, err = Open(a.home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	recovered, err := a.dispatchScheduled(tctx(t), s.ID)
	var after string
	if err != nil || recovered.State != ScheduledSent || scheduleCopies(t, a, s) != 1 {
		t.Fatal("restart recovery", recovered, err)
	}
	if err = a.store.db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND lid=?`, conv, s.ID).Scan(&after); err != nil || after != sealed {
		t.Fatal("restart resealed the schedule", err)
	}
}

func TestScheduledSendReservationAfterQueuedPlanning(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	a := w.alice
	stops[a]()
	conv := p.State.Conv
	in := futureSchedule(conv)
	in.ID = protocol.NewID()
	previous := beforeOutbox
	defer func() { beforeOutbox = previous }()
	reserved := false
	beforeOutbox = func() {
		beforeOutbox = previous
		if _, err := a.ScheduleSend(in); err != nil {
			t.Fatal(err)
		}
		reserved = true
	}
	if _, err := a.SendConv(WithQueuedSend(tctx(t), in.ID), conv, ConvOutgoing{Body: "planned before reservation"}); err == nil {
		t.Fatal("ordinary commit ignored a schedule reserved after planning")
	}
	s := scheduleOf(t, a, in.ID)
	if !reserved || s.State != ScheduledPending || scheduleCopies(t, a, s) != 0 {
		t.Fatal("reservation race installed an ordinary copy", s)
	}
}

func TestScheduledSendLifecycle(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	in := futureSchedule(conv)
	s, e := a.ScheduleSend(in)
	if e != nil {
		t.Fatal(e)
	}
	if s.State != ScheduledPending || s.Rev != 1 || !s.Due.Equal(in.Due) || s.Timezone != in.Timezone || len(s.Audience.Persons) != 2 || scheduleCopies(t, a, s) != 0 {
		t.Fatalf("future %+v", s)
	}
	next, e := a.scheduledDue(tctx(t), time.Now())
	if e != nil || !next.Equal(s.Due) || scheduleCopies(t, a, s) != 0 {
		t.Fatalf("early %s %v", next, e)
	}
	next, e = a.scheduledDue(tctx(t), time.Now().Add(3*time.Hour))
	if e != nil || !next.Equal(s.Due) || scheduleCopies(t, a, s) != 0 || scheduleOf(t, a, s.ID).Rev != 1 {
		t.Fatal("clock rollback/fast query dispatched early", next, e)
	}
	// Even a caller with a fast/faulty clock cannot bypass the real commit clock.
	if _, e = a.dispatchScheduled(tctx(t), s.ID); !errors.Is(e, errScheduledEarly) {
		t.Fatal(e)
	}
	in.Body = "edited"
	in.Due = in.Due.Add(time.Hour)
	s, e = a.EditScheduledSend(s.ID, s.Rev, in)
	if e != nil || s.Rev != 2 {
		t.Fatal(s, e)
	}
	if _, e = a.CancelScheduledSend(s.ID, 1); !errors.Is(e, ErrScheduledStale) {
		t.Fatal("stale cancel", e)
	}
	sent, e := a.SendScheduledNow(tctx(t), s.ID, s.Rev)
	if e != nil || sent.State != ScheduledSent || scheduleCopies(t, a, s) != 1 {
		t.Fatal(sent, e)
	}
	var kind, body, lid string
	a.store.db.QueryRow(`SELECT kind,body,lid FROM outbox WHERE conv=? AND lid=?`, conv, s.ID).Scan(&kind, &body, &lid)
	if kind != envelope.KindMessage || body != "edited" || lid != s.ID {
		t.Fatal(kind, body, lid)
	}
	if _, e = a.SendScheduledNow(tctx(t), s.ID, sent.Rev); !errors.Is(e, ErrScheduledStale) {
		t.Fatal("sent now twice", e)
	}
	if _, e = a.dispatchScheduled(tctx(t), s.ID); e != nil || scheduleCopies(t, a, s) != 1 {
		t.Fatal("replay", e)
	}
	cancelled, e := a.ScheduleSend(futureSchedule(conv))
	if e != nil {
		t.Fatal(e)
	}
	cancelled, e = a.CancelScheduledSend(cancelled.ID, cancelled.Rev)
	if e != nil || cancelled.State != ScheduledCancelled {
		t.Fatal(cancelled, e)
	}
	if _, e = a.EditScheduledSend(cancelled.ID, cancelled.Rev, in); !errors.Is(e, ErrScheduledStale) {
		t.Fatal(e)
	}
	list, e := a.ScheduledSends(false)
	if e != nil || len(list) != 0 {
		t.Fatal(list, e)
	}
	for _, bad := range []ScheduledSendInput{{Conv: conv, Body: "x", Due: time.Now().Add(-time.Second)}, {Conv: conv, Body: "x", Due: time.Now().Add(time.Hour), Timezone: "bad timezone"}, {Conv: conv, Body: "x", Topic: "new", Due: time.Now().Add(time.Hour)}} {
		if _, e = a.ScheduleSend(bad); e == nil {
			t.Fatal("invalid schedule saved")
		}
	}
}
func TestScheduledSendFilesAndGC(t *testing.T) {
	for _, erase := range []bool{false, true} {
		t.Run(map[bool]string{false: "retract", true: "erase"}[erase], func(t *testing.T) {
			w, conv := scheduledWorld(t)
			a := w.alice
			path, data := writeFile(t, t.TempDir(), "kept.bin", 3000)
			prior, e := a.SendConv(WithQueuedSend(tctx(t), ""), conv, ConvOutgoing{Body: "earlier file", Files: []OutgoingFile{{Path: path}}})
			if e != nil {
				t.Fatal(e)
			}
			in := futureSchedule(conv)
			in.Files = []OutgoingFile{{Path: path, Name: "scheduled.bin"}}
			s, e := a.ScheduleSend(in)
			if e != nil {
				t.Fatal(e)
			}
			var raw string
			a.store.db.QueryRow(`SELECT files FROM scheduled_sends WHERE id=?`, s.ID).Scan(&raw)
			if strings.Contains(raw, path) || !strings.Contains(raw, sha(data)) {
				t.Fatal("not durable descriptors", raw)
			}
			if erase {
				if _, e = a.DeleteConversation(tctx(t), conv); e != nil {
					t.Fatal(e)
				}
			} else {
				if _, e = a.Retract(tctx(t), ControlRef{Conv: conv, ID: prior.LID, Fingerprint: a.Self().Fingerprint()}, ""); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = os.Stat(a.keptPath(sha(data))); e != nil {
				t.Fatal("GC deleted scheduled attachment", e)
			}
			if e = os.Remove(path); e != nil {
				t.Fatal(e)
			}
			sent, e := a.SendScheduledNow(tctx(t), s.ID, s.Rev)
			if e != nil || sent.State != ScheduledSent {
				t.Fatal(sent, e)
			}
			var id string
			a.store.db.QueryRow(`SELECT id FROM outbox WHERE conv=? AND lid=? LIMIT 1`, conv, s.ID).Scan(&id)
			r, f, e := a.OpenSentAttachment(tctx(t), id, 0)
			if e != nil {
				t.Fatal(e)
			}
			if !bytes.Equal(readAll(t, r), data) || f.Name != "scheduled.bin" {
				t.Fatal("scheduled bytes/name changed")
			}
			assertOnlyFiles(t, filepath.Join(a.home, "opened"))
		})
	}
}
func TestScheduledSendMissingAndTamperedFiles(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	for _, tamper := range []bool{false, true} {
		path, _ := writeFile(t, t.TempDir(), "file.bin", 400)
		in := futureSchedule(conv)
		in.Files = []OutgoingFile{{Path: path}}
		s, e := a.ScheduleSend(in)
		if e != nil {
			t.Fatal(e)
		}
		kept := a.keptPath(s.Files[0].SHA256)
		if tamper {
			e = os.WriteFile(kept, []byte("tampered"), 0600)
		} else {
			e = os.Remove(kept)
		}
		if e != nil {
			t.Fatal(e)
		}
		result, e := a.SendScheduledNow(tctx(t), s.ID, s.Rev)
		if e == nil || result.State != ScheduledNeedsReview || scheduleCopies(t, a, s) != 0 {
			t.Fatal(result, e)
		}
		edit := in
		edit.Files = nil
		if _, e = a.EditScheduledSend(s.ID, result.Rev, edit); e == nil {
			t.Fatal("rescheduled against missing/corrupt retained content")
		}
		if tamper {
			if _, e = a.ScheduleSend(in); e == nil {
				t.Fatal("saved against corrupt existing kept file")
			}
		}
	}
	assertOnlyFiles(t, filepath.Join(a.home, "opened"))
}

func TestScheduledSendNowOwnsDispatchBeforeWake(t *testing.T) {
	w, conv := scheduledWorld(t)
	// A separate local API process has its own store callback, independent of
	// the stopped daemon's remaining background activity.
	a, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	path, _ := writeFile(t, t.TempDir(), "file.bin", 400)
	in := futureSchedule(conv)
	in.Files = []OutgoingFile{{Path: path}}
	s, err := a.ScheduleSend(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(a.keptPath(s.Files[0].SHA256)); err != nil {
		t.Fatal(err)
	}
	changed := a.store.onChange
	woke := false
	a.store.onChange = func() {
		changed()
		if woke {
			return
		}
		woke = true
		// Deterministically let another dispatcher take a just-due schedule
		// if the manual caller has not yet acquired its cross-process lock.
		release, e := lockfile.Acquire(filepath.Join(a.home, "scheduled-send.lock"))
		if errors.Is(e, lockfile.ErrLocked) {
			return
		}
		if e != nil {
			t.Fatal(e)
		}
		release()
		if _, e := a.dispatchScheduled(tctx(t), s.ID); e == nil {
			t.Fatal("competing dispatcher did not reject the missing attachment")
		}
	}
	result, err := a.SendScheduledNow(tctx(t), s.ID, s.Rev)
	a.store.onChange = changed
	if !woke || err == nil || result.State != ScheduledNeedsReview || scheduleCopies(t, a, s) != 0 {
		t.Fatal("manual dispatch lost the attempt's rejection", result, err, woke)
	}
}

// Exercise the real neutral transaction hook: failures after it must also roll
// back sent state, and a changed revision/deadline cannot commit any copies.
func TestScheduledSendAtomicGuard(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	for _, mode := range []string{"revision", "deadline", "precommit", "clock-rollback"} {
		t.Run(mode, func(t *testing.T) {
			s, e := a.ScheduleSend(futureSchedule(conv))
			if e != nil {
				t.Fatal(e)
			}
			s.Due = time.Now().Add(-time.Second)
			if mode == "clock-rollback" {
				s.Due = time.Now().Add(time.Hour)
			}
			s.State = ScheduledDispatching
			if _, e = a.store.db.Exec(`UPDATE scheduled_sends SET state='dispatching',due_at=? WHERE id=?`, s.Due.UnixNano(), s.ID); e != nil {
				t.Fatal(e)
			}
			if mode == "revision" {
				a.store.db.Exec(`UPDATE scheduled_sends SET rev=rev+1 WHERE id=?`, s.ID)
			}
			if mode == "deadline" {
				a.store.db.Exec(`UPDATE scheduled_sends SET due_at=? WHERE id=?`, time.Now().Add(time.Hour).UnixNano(), s.ID)
			}
			guard := a.scheduledCommit(s)
			injected := errors.New("injected before commit")
			ctx := context.WithValue(WithQueuedSend(tctx(t), s.ID), scheduledDispatchKey{}, s.ID)
			_, e = a.SendConv(ctx, conv, ConvOutgoing{Body: s.Body, beforeStore: func(tx *sql.Tx, c []outCopy, l envelope.Inner) error {
				if err := guard(tx, c, l); err != nil {
					return err
				}
				var state string
				if err := tx.QueryRow(`SELECT state FROM scheduled_sends WHERE id=?`, s.ID).Scan(&state); err != nil {
					return err
				}
				if state != ScheduledSent {
					t.Error("guard did not stage sent in same transaction")
				}
				return injected
			}})
			if e == nil || scheduleCopies(t, a, s) != 0 || scheduleOf(t, a, s.ID).State == ScheduledSent {
				t.Fatal("partial atomic commit", e)
			}
			want := ErrScheduledStale
			if mode == "precommit" {
				want = injected
			}
			if mode == "clock-rollback" {
				want = errScheduledEarly
			}
			if !errors.Is(e, want) {
				t.Fatal("wrong atomic rejection", mode, e)
			}
		})
	}
}
func TestScheduledSendRestartReconciliation(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	s, e := a.ScheduleSend(futureSchedule(conv))
	if e != nil {
		t.Fatal(e)
	}
	// Crash before any outbox commit: dispatching and same durable identity survive.
	due := time.Now().Add(-time.Second)
	a.store.db.Exec(`UPDATE scheduled_sends SET state='dispatching',due_at=? WHERE id=?`, due.UnixNano(), s.ID)
	home := a.home
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	a, e = Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	got, e := a.dispatchScheduled(tctx(t), s.ID)
	if e != nil || got.State != ScheduledSent || scheduleCopies(t, a, s) != 1 {
		t.Fatal(got, e)
	}
	// Simulate legacy/postcommit crash state: outbox LID already committed.
	a.store.db.Exec(`UPDATE scheduled_sends SET state='dispatching' WHERE id=?`, s.ID)
	a.stopBackgroundPosts()
	if e = a.Close(); e != nil {
		t.Fatal(e)
	}
	a, e = Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	got, e = a.dispatchScheduled(tctx(t), s.ID)
	if e != nil || got.State != ScheduledSent || scheduleCopies(t, a, s) != 1 {
		t.Fatal("replayed committed send", got, e)
	}
}
func addScheduledMember(t *testing.T, w *world, p GroupContext, reader *Agent, installOnly ...bool) GroupContext {
	t.Helper()
	person, _, e := reader.store.selfPerson(reader.Address)
	if e != nil {
		t.Fatal(e)
	}
	admission, e := reader.SignGroupAdmission(p.Root, p.State.Seq+1, p.State.Hash(), nil)
	if e != nil {
		t.Fatal(e)
	}
	next := p
	next.Proof = nil
	next.State.Seq++
	next.State.Prev = p.State.Hash()
	next.State.Members = slices.Clone(p.State.Members)
	next.State.Members = append(next.State.Members, protocol.GroupMember{ConvMember: protocol.ConvMember{Person: person.roster.Person, Roster: person.roster.Hash()}, Admission: admission})
	slices.SortFunc(next.State.Members, func(a, b protocol.GroupMember) int { return strings.Compare(a.Person, b.Person) })
	next, e = w.alice.SignGroupState(tctx(t), next)
	if e != nil {
		t.Fatal(e)
	}
	commit, e := w.alice.BuildGroupCommit(tctx(t), next)
	if e != nil {
		t.Fatal(e)
	}
	if len(installOnly) > 0 && installOnly[0] {
		// Inject already signed/verified ingress state, not a nested send while
		// the sender holds its spool lock. Normal authority must see it.
		if e = w.alice.IngestGroupProofPage(tctx(t), next.Root, protocol.GroupJournalPage{Records: []protocol.GroupCommit{commit}}); e != nil {
			t.Fatal(e)
		}
		tx, e := w.alice.store.db.Begin()
		if e != nil {
			t.Fatal(e)
		}
		if e = installGroupContext(tx, next); e != nil {
			tx.Rollback()
			t.Fatal(e)
		}
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		return next
	}
	if _, e = w.alice.PublishGroup(tctx(t), commit, next); e != nil {
		t.Fatal(e)
	}
	return next
}
func TestScheduledSendAudienceGrowth(t *testing.T) {
	for _, afterPlan := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-plan", true: "after-plan"}[afterPlan], func(t *testing.T) {
			w, _, p, stops := groupTurnsFixture(t)
			a := w.alice
			stops[a]()
			s, e := a.ScheduleSend(futureSchedule(p.State.Conv))
			if e != nil {
				t.Fatal(e)
			}
			dana := proofReader(t, w, "scheduled-reader")
			if afterPlan {
				previous := beforeOutbox
				beforeOutbox = func() { beforeOutbox = previous; addScheduledMember(t, w, p, dana, true) }
				defer func() { beforeOutbox = previous }()
			} else {
				addScheduledMember(t, w, p, dana)
				// The approved snapshot predates SendConv's recipient plan. The
				// normal sender now seals a copy for the newly admitted person;
				// only the neutral schedule guard can stop that fresh valid plan.
				s.Due = time.Now().Add(-time.Second)
				if _, e = a.store.db.Exec(`UPDATE scheduled_sends SET state='dispatching',due_at=? WHERE id=?`, s.Due.UnixNano(), s.ID); e != nil {
					t.Fatal(e)
				}
				sawNew := false
				guard := a.scheduledCommit(s)
				ctx := context.WithValue(WithQueuedSend(tctx(t), s.ID), scheduledDispatchKey{}, s.ID)
				_, e = a.SendConv(ctx, s.Conv, ConvOutgoing{Body: s.Body, beforeStore: func(tx *sql.Tx, copies []outCopy, local envelope.Inner) error {
					for _, c := range copies {
						sawNew = sawNew || c.env.To == dana.Address
					}
					return guard(tx, copies, local)
				}})
				if !errors.Is(e, ErrScheduledAudience) || !sawNew || scheduleCopies(t, a, s) != 0 {
					t.Fatal("prechecked audience grew in actual sealed plan", e, sawNew)
				}
			}
			var got ScheduledSend
			if afterPlan {
				got, e = a.SendScheduledNow(tctx(t), s.ID, s.Rev)
			} else {
				got, e = a.dispatchScheduled(tctx(t), s.ID)
			}
			if e == nil || got.State != ScheduledNeedsReview || scheduleCopies(t, a, s) != 0 {
				t.Fatalf("audience growth installed copies %+v %v", got, e)
			}
		})
	}
}
func TestScheduledSendNewApprovedDevice(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	s, e := a.ScheduleSend(futureSchedule(conv))
	if e != nil {
		t.Fatal(e)
	}
	phone, await, _ := linkPhone(t, w.bob, "scheduled-phone")
	request := pendingLink(t, w.bob)
	if e = w.bob.DecideLink(tctx(t), request.ID, true); e != nil {
		t.Fatal(e)
	}
	if out := <-await; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	got, e := a.SendScheduledNow(tctx(t), s.ID, s.Rev)
	if e != nil || got.State != ScheduledSent || scheduleCopies(t, a, s) != 2 {
		t.Fatal(got, e)
	}
	var n int
	a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND recipient=?`, s.ID, phone.Address).Scan(&n)
	if n != 1 {
		t.Fatal("new approved person's device was omitted")
	}
}

// A stopped sender promises no deadline execution. Its existing human-only
// daemon starts the same reminder deadline loop without configuring a harness.
func TestScheduledSendHumanOnlyDeadline(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	s, e := a.ScheduleSend(futureSchedule(conv))
	if e != nil {
		t.Fatal(e)
	}
	due := time.Now().Add(250 * time.Millisecond)
	a.store.db.Exec(`UPDATE scheduled_sends SET due_at=? WHERE id=?`, due.UnixNano(), s.ID)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = a.Run(ctx, RunOptions{HumanOnly: true}); close(done) }()
	defer func() { cancel(); <-done }()
	// Read count before time, so a delayed test wake cannot make a false early claim.
	for {
		n := scheduleCopies(t, a, s)
		now := time.Now()
		if !now.Before(due) {
			break
		}
		if n != 0 {
			t.Fatal("early outbox")
		}
		time.Sleep(10 * time.Millisecond)
	}
	eventually(t, "scheduled wall timer", func() bool { return scheduleOf(t, a, s.ID).State == ScheduledSent })
	if scheduleCopies(t, a, s) != 1 {
		t.Fatal("deadline repeated")
	}
}

func TestScheduledSendSealedRecipientFingerprint(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	for _, binding := range []bool{false, true} {
		seen := 0
		out := ConvOutgoing{Body: "exact sealing key", beforeStore: func(_ *sql.Tx, copies []outCopy, local envelope.Inner) error {
			for _, c := range copies {
				seen++
				if c.env.To != w.bob.Address || c.recipientFP != w.bob.Self().Fingerprint() {
					t.Fatalf("actual ordinary DM sealed key missing: binding=%v fp=%q", binding, c.recipientFP)
				}
			}
			return nil
		}}
		if binding {
			out.ReplyReceiver = &ReplyReceiver{Kind: "human"}
		}
		sent, e := a.SendConv(WithQueuedSend(tctx(t), ""), conv, out)
		if e != nil || seen != 1 {
			t.Fatal("normal DM/binding control", sent, e, seen)
		}
		var fp string
		if e = a.store.db.QueryRow(`SELECT recipient_fp FROM outbox WHERE id=?`, sent.ID).Scan(&fp); e != nil || fp != w.bob.Self().Fingerprint() {
			t.Fatal("stored sealed key", fp, e)
		}
	}
}

func TestScheduledSendGuestTopicAndAudience(t *testing.T) {
	w, guest, conv, _, stub := humanWorld(t)
	a := w.alice
	root, e := a.SendConv(tctx(t), conv, ConvOutgoing{Body: "scheduled named scope", Topic: "new"})
	if e != nil {
		t.Fatal(e)
	}
	topic := convMsgByID(t, a, conv, root.ID).Topic
	named := futureSchedule(conv)
	named.Topic = topic
	old, e := a.ScheduleSend(named)
	if e != nil {
		t.Fatal(e)
	}
	main, e := a.ScheduleSend(futureSchedule(conv))
	if e != nil {
		t.Fatal(e)
	}
	p, e := a.InviteHumanInScope(tctx(t), conv, guest.Address, nil, "", &topic)
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "scoped guest invitation", func() bool { return stateAt(t, guest, p.PID).State == PartInvited })
	if _, e = guest.AcceptParticipation(tctx(t), p.PID); e != nil {
		t.Fatal(e)
	}
	eventually(t, "scoped guest acceptance", func() bool { return stateAt(t, a, p.PID).HumanActive() })
	stalePreview := named
	stalePreview.Audience = &old.Audience
	if _, e = a.ScheduleSend(stalePreview); !errors.Is(e, ErrScheduledAudience) {
		t.Fatal("saved a stale approved audience preview", e)
	}
	old, e = a.EditScheduledSend(old.ID, old.Rev, named)
	if e != nil || slices.Contains(old.Audience.PIDs, p.PID) {
		t.Fatal("ordinary edit silently widened approved audience", old, e)
	}
	// A new named-topic guest cannot widen the unrelated Main schedule.
	got, e := a.SendScheduledNow(tctx(t), main.ID, main.Rev)
	if e != nil || got.State != ScheduledSent {
		t.Fatal("unrelated topic changed", got, e)
	}
	got, e = a.SendScheduledNow(tctx(t), old.ID, old.Rev)
	if !errors.Is(e, ErrScheduledAudience) || got.State != ScheduledNeedsReview || scheduleCopies(t, a, old) != 0 {
		t.Fatal("topic guest growth leaked", got, e)
	}
	// Explicit review/edit captures the newly approved exact PID and uses the
	// captured-human normal sender, with no execution request or native job.
	approved, e := a.ScheduleAudience(conv, topic)
	if e != nil {
		t.Fatal(e)
	}
	named.Audience = &approved
	reviewed, e := a.EditScheduledSend(old.ID, got.Rev, named)
	if e != nil || !slices.Contains(reviewed.Audience.PIDs, p.PID) {
		t.Fatal(reviewed, e)
	}
	got, e = a.SendScheduledNow(tctx(t), reviewed.ID, reviewed.Rev)
	if e != nil || got.State != ScheduledSent {
		t.Fatal("reviewed guest audience", got, e)
	}
	retarget, e := a.ScheduleSend(named)
	if e != nil {
		t.Fatal(e)
	}
	toMain := futureSchedule(conv)
	if _, e = a.EditScheduledSend(retarget.ID, retarget.Rev, toMain); !errors.Is(e, ErrScheduledAudience) {
		t.Fatal("scope retarget silently refreshed audience", e)
	}
	mainAudience, e := a.ScheduleAudience(conv, "")
	if e != nil {
		t.Fatal(e)
	}
	toMain.Audience = &mainAudience
	retarget, e = a.EditScheduledSend(retarget.ID, retarget.Rev, toMain)
	if e != nil || retarget.Topic != "" || len(retarget.Audience.PIDs) != 0 {
		t.Fatal("explicit reviewed scope retarget", retarget, e)
	}
	named.Audience = nil // the guest approves its own author scope
	own, e := guest.ScheduleSend(named)
	if e != nil || own.Audience.AuthorPID != p.PID {
		t.Fatal("guest schedule", own, e)
	}
	if _, e = guest.ScheduleSend(futureSchedule(conv)); e == nil {
		t.Fatal("guest scheduled outside named topic")
	}
	got, e = guest.SendScheduledNow(tctx(t), own.ID, own.Rev)
	if e != nil || got.State != ScheduledSent {
		t.Fatal("guest human dispatch", got, e)
	}
	if stub.runs() != 0 {
		t.Fatal("ordinary scheduled message started a native job")
	}
}
func TestScheduledSendUnavailableContextWaits(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	a := w.alice
	stops[a]()
	s, e := a.ScheduleSend(futureSchedule(p.State.Conv))
	if e != nil {
		t.Fatal(e)
	}
	if e = a.NoteGroupHead(protocol.GroupHead{Conv: p.State.Conv, Bootstrap: p.Root.Creator.Fingerprint, Seq: p.State.Seq + 1, Hash: strings.Repeat("a", 64)}); e != nil {
		t.Fatal(e)
	}
	if _, e = a.store.db.Exec(`UPDATE scheduled_sends SET due_at=? WHERE id=?`, time.Now().Add(-time.Second).UnixNano(), s.ID); e != nil {
		t.Fatal(e)
	}
	next, e := a.scheduledDue(tctx(t), time.Now())
	if e != nil || !next.IsZero() {
		t.Fatal("overdue hot timer", next, e)
	}
	got := scheduleOf(t, a, s.ID)
	if got.State != ScheduledPending || got.Detail == "" || scheduleCopies(t, a, s) != 0 {
		t.Fatal(got)
	}
	before, _ := a.Changed()
	next, e = a.scheduledDue(tctx(t), time.Now())
	after, _ := a.Changed()
	if e != nil || !next.IsZero() || before != after {
		t.Fatal("unchanged unavailable planner spins its own wake", next, e, before, after)
	}
}
func TestScheduledSendConcurrentCAS(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	for i := 0; i < 6; i++ {
		s, e := a.ScheduleSend(futureSchedule(conv))
		if e != nil {
			t.Fatal(e)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		go func() { <-start; _, err := a.SendScheduledNow(tctx(t), s.ID, s.Rev); results <- err }()
		go func() { <-start; _, err := a.CancelScheduledSend(s.ID, s.Rev); results <- err }()
		close(start)
		one, two := <-results, <-results
		if (one == nil) == (two == nil) || one != nil && !errors.Is(one, ErrScheduledStale) || two != nil && !errors.Is(two, ErrScheduledStale) {
			t.Fatal("CAS did not choose exactly one", one, two)
		}
		got := scheduleOf(t, a, s.ID)
		copies := scheduleCopies(t, a, s)
		if got.State == ScheduledSent && copies != 1 || got.State == ScheduledCancelled && copies != 0 || got.State != ScheduledSent && got.State != ScheduledCancelled {
			t.Fatal("CAS result", got, copies)
		}
	}
}

// Lost-response retries prove the same content without a second keep, audience
// capture or write. The saved state (including terminal states) is authoritative.
func TestScheduledCreateIdempotent(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	path := filepath.Join(t.TempDir(), "retry.bin")
	if err := os.WriteFile(path, []byte("abc"), 0600); err != nil {
		t.Fatal(err)
	}
	in := futureSchedule(conv)
	in.ID = protocol.NewID()
	in.Files = []OutgoingFile{{Path: path, Name: "retry.bin"}}
	saved, err := a.ScheduleSend(in)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != in.ID {
		t.Fatal("ignored supplied ID")
	}
	// The retry needs its actual input file, but cannot touch kept bytes.
	kept := filepath.Join(a.home, "kept")
	if err = os.Rename(kept, kept+"-parked"); err != nil {
		t.Fatal(err)
	}
	defer os.Rename(kept+"-parked", kept)
	for _, state := range []string{ScheduledPending, ScheduledDispatching, ScheduledSent, ScheduledCancelled, ScheduledNeedsReview} {
		past := time.Now().Add(-time.Hour).UTC()
		in.Due = past
		if _, err = a.store.db.Exec(`UPDATE scheduled_sends SET state=?,due_at=?,rev=17 WHERE id=?`, state, past.UnixNano(), in.ID); err != nil {
			t.Fatal(err)
		}
		before, wake := a.ViewChanges()
		got, e := a.ScheduleSend(in)
		want := scheduleOf(t, a, in.ID)
		if e != nil || got.ID != saved.ID || got.State != state || got.Rev != 17 || !got.Due.Equal(past) || !slices.Equal(got.Files, want.Files) {
			t.Fatal(got, e)
		}
		after, _ := a.ViewChanges()
		if after != before {
			t.Fatal("retry changed view revision")
		}
		select {
		case <-wake:
			t.Fatal("retry emitted push")
		default:
		}
	}
	original := scheduleOf(t, a, in.ID)
	for _, change := range []func(*ScheduledSendInput){func(x *ScheduledSendInput) { x.Body += "different" }, func(x *ScheduledSendInput) { x.Due = x.Due.Add(time.Second) }, func(x *ScheduledSendInput) { x.Topic = protocol.NewID() }, func(x *ScheduledSendInput) { x.Files = []OutgoingFile{} }, func(x *ScheduledSendInput) { x.Files = []OutgoingFile{{Path: path, Name: "other.bin"}} }} {
		other := in
		change(&other)
		if _, e := a.ScheduleSend(other); !errors.Is(e, ErrScheduledIDConflict) {
			t.Fatal("different retry", e)
		}
	}
	// Same name and length is insufficient proof of the actual bytes.
	if err = os.WriteFile(path, []byte("xyz"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, e := a.ScheduleSend(in); !errors.Is(e, ErrScheduledIDConflict) {
		t.Fatal("same-size different bytes", e)
	}
	got := scheduleOf(t, a, in.ID)
	if got.State != original.State || got.Rev != original.Rev || got.Body != original.Body {
		t.Fatal("conflict overwrote record")
	}
}
func TestScheduledCreateIDValidationAndCollision(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	in := futureSchedule(conv)
	for _, id := range []string{"bad", strings.Repeat("A", 32), strings.Repeat("a", 64)} {
		in.ID = id
		if _, e := a.ScheduleSend(in); !errors.Is(e, ErrScheduledIDConflict) {
			t.Fatal(id, e)
		}
	}
	in.ID = protocol.NewID()
	s, e := a.ScheduleSend(in)
	if e != nil {
		t.Fatal(e)
	}
	in.ID = protocol.NewID()
	if _, e = a.EditScheduledSend(s.ID, s.Rev, in); !errors.Is(e, ErrScheduledIDConflict) {
		t.Fatal("edit replaced ID", e)
	}
	in.ID = s.ID
	if _, e = a.EditScheduledSend(s.ID, s.Rev, in); e != nil {
		t.Fatal("matching edit ID", e)
	}
	out, e := a.SendConv(WithQueuedSend(tctx(t), ""), conv, ConvOutgoing{Body: "ordinary"})
	if e != nil {
		t.Fatal(e)
	}
	in.ID = out.LID
	if _, e = a.ScheduleSend(in); !errors.Is(e, ErrScheduledIDConflict) {
		t.Fatal("outbox LID collision", e)
	}
	// Exercise the separate local-journal collision seam. Ordinary human DM
	// sends have outbox rows only; a local inbox record is a distinct case.
	in.ID = protocol.NewID()
	if _, e = a.store.db.Exec(`INSERT INTO inbox(id,sender,ts,kind,body,received_at,conv,lid,local,verified_by) VALUES(?,?,?,'message','local journal',?,?,?,1,?)`, protocol.NewID(), a.Address, time.Now().Unix(), time.Now().Unix(), conv, in.ID, a.Self().Fingerprint()); e != nil {
		t.Fatal(e)
	}
	if _, e = a.ScheduleSend(in); !errors.Is(e, ErrScheduledIDConflict) {
		t.Fatal("local inbox LID collision", e)
	}

}
func TestScheduledCreateConcurrentIdentical(t *testing.T) {
	w, conv := scheduledWorld(t)
	a := w.alice
	in := futureSchedule(conv)
	in.ID = protocol.NewID()
	start := make(chan struct{})
	type result struct {
		s ScheduledSend
		e error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { <-start; s, e := a.ScheduleSend(in); results <- result{s, e} }()
	}
	close(start)
	for i := 0; i < 2; i++ {
		r := <-results
		if r.e != nil || r.s.ID != in.ID || r.s.Rev != 1 {
			t.Fatal(r)
		}
	}
	rows, e := a.ScheduledSends(true)
	if e != nil || len(rows) != 1 {
		t.Fatal(rows, e)
	}
}
