package client

import (
	"bytes"
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
	"github.com/misunders2d/agentnet/internal/lockfile"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// stallPersonChains holds a's person-chain requests at the Hub until the
// returned release, the exact stall of the 2026-11-04 incident.
func stallPersonChains(t *testing.T, w *world, a *Agent) func() {
	t.Helper()
	release := make(chan struct{})
	var once sync.Once
	w.hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == a.Address && strings.HasPrefix(r.URL.Path, "/v1/persons/") {
			<-release
		}
	})
	free := func() {
		once.Do(func() {
			w.hub.SetHold(nil)
			close(release)
		})
	}
	t.Cleanup(free)
	return free
}

func queuedLIDCopies(t *testing.T, a *Agent, lid string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=?`, lid).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func queuedRow(t *testing.T, a *Agent, id string) ScheduledSend {
	t.Helper()
	s, found, err := a.ScheduledSend(id)
	if err != nil || !found {
		t.Fatalf("accepted message %s not kept: %v %v", id, found, err)
	}
	return s
}

func plainWords(t *testing.T, words string) {
	t.Helper()
	for _, technical := range []string{"http", "context", "deadline", "chain?", "/v1/", "dial "} {
		if strings.Contains(strings.ToLower(words), technical) {
			t.Fatalf("technical text in what the person reads: %q", words)
		}
	}
}

// The incident as it was: the ordinary sender under a stalled chain answers
// with its transport error and keeps nothing. This stays the fallback's
// behavior, which is why the accepted path must not depend on it.
func TestQueuedSendReproOrdinarySenderLosesToStall(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	stallPersonChains(t, w, w.alice)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	id := protocol.NewID()
	_, err := w.alice.SendConv(WithQueuedSend(ctx, id), p.State.Conv, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: "lost to the stall"})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "/chain") {
		t.Fatalf("not the incident's failure: %v", err)
	}
	if queuedLIDCopies(t, w.alice, id) != 0 {
		t.Fatal("the ordinary sender stored something after all")
	}
}

// Regression 1. Accepting needs neither the network nor the spool lock. Stalls
// and busy catch-up leave text, reply, scope and file in place, visibly waiting,
// and the same id goes out once when the stall ends.
func TestQueuedSendOutlivesStallsAndBusySpool(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	conv := p.State.Conv
	first, err := w.alice.SendConv(WithQueuedSend(tctx(t), protocol.NewID()), conv, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: "to reply to"})
	if err != nil {
		t.Fatal(err)
	}
	stops[w.alice]()
	w.alice.queued.budget = 200 * time.Millisecond
	free := stallPersonChains(t, w, w.alice)
	spool, err := lockfile.Wait(w.alice.spoolLockPath())
	if err != nil {
		t.Fatal(err)
	}
	released := false
	unlock := func() {
		if !released {
			released = true
			spool()
		}
	}
	defer unlock()
	path := filepath.Join(t.TempDir(), "note.txt")
	if err = os.WriteFile(path, []byte("attached"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := protocol.NewID()
	started := time.Now()
	res, err := w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: conv, Body: "accepted during the stall", ReplyTo: first.LID, Quote: first.LID, Files: []OutgoingFile{{Name: "note.txt", Path: path}}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("accepting waited on the stall: %v", took)
	}
	if res.Row.ID != id || res.Row.State != ScheduledPending {
		t.Fatalf("accepted as %+v", res.Row)
	}
	s := queuedRow(t, w.alice, id)
	if s.Body != "accepted during the stall" || s.ReplyTo != first.LID || s.Quote != first.LID || len(s.Files) != 1 || s.Files[0].Name != "note.txt" || s.Conv != conv || len(s.Audience.Persons) == 0 {
		t.Fatalf("the accepted message changed: %+v", s)
	}
	unlock()
	// Attempt after attempt fails on time; nothing cancels the message.
	eventually(t, "a failed attempt noted", func() bool { return queuedRow(t, w.alice, id).Diagnostic != "" })
	s = queuedRow(t, w.alice, id)
	if s.State != ScheduledPending && s.State != ScheduledDispatching {
		t.Fatalf("a stall stopped the message: %+v", s)
	}
	plainWords(t, s.Detail)
	if !strings.Contains(s.Diagnostic, "/chain") {
		t.Fatalf("the technical cause is not in the details: %q", s.Diagnostic)
	}
	if queuedLIDCopies(t, w.alice, id) != 0 {
		t.Fatal("stored before the stall ended")
	}
	free()
	eventually(t, "the send after the stall", func() bool { return queuedLIDCopies(t, w.alice, id) > 0 })
	var reply, body string
	if err = w.alice.store.db.QueryRow(`SELECT coalesce(reply_to,''),body FROM outbox WHERE lid=? LIMIT 1`, id).Scan(&reply, &body); err != nil {
		t.Fatal(err)
	}
	if reply != first.LID || body != "accepted during the stall" {
		t.Fatalf("sent with reply %q body %q", reply, body)
	}
	eventually(t, "the finished record gone", func() bool { _, found, _ := w.alice.ScheduledSend(id); return !found })
}

// A stall in one chat leaves other chats free, and one chat keeps its order.
func TestQueuedSendStallHoldsNoOtherChatAndKeepsOrder(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	dm := newDM(t, w.alice, w.bob)
	w.alice.queued.budget = 200 * time.Millisecond
	release := make(chan struct{})
	var once sync.Once
	free := func() {
		once.Do(func() {
			w.hub.SetHold(nil)
			close(release)
		})
	}
	defer free()
	stalled := false
	var mu sync.Mutex
	w.hub.SetHold(func(r *http.Request) {
		mu.Lock()
		hold := stalled
		mu.Unlock()
		if hold && r.Header.Get(protocol.HeaderAgent) == w.alice.Address && strings.HasPrefix(r.URL.Path, "/v1/persons/") {
			<-release
		}
	})
	mu.Lock()
	stalled = true
	mu.Unlock()
	one, two := protocol.NewID(), protocol.NewID()
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: one, Conv: p.State.Conv, Body: "first"}, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: two, Conv: p.State.Conv, Body: "second"}, "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first attempt failed", func() bool { return queuedRow(t, w.alice, one).Diagnostic != "" })
	if queuedRow(t, w.alice, two).Diagnostic != "" {
		t.Fatal("a later message overtook the waiting one")
	}
	// Another chat is not behind this one: its person is not the one stalled.
	mu.Lock()
	stalled = false
	mu.Unlock()
	other := protocol.NewID()
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: other, Conv: dm, Body: "another chat"}, "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the other chat went at once", func() bool { return queuedLIDCopies(t, w.alice, other) > 0 })
	free()
	eventually(t, "both sent", func() bool { return queuedLIDCopies(t, w.alice, one) > 0 && queuedLIDCopies(t, w.alice, two) > 0 })
	var a, b int64
	if err := w.alice.store.db.QueryRow(`SELECT min(rowid) FROM outbox WHERE lid=?`, one).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.store.db.QueryRow(`SELECT min(rowid) FROM outbox WHERE lid=?`, two).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if a >= b {
		t.Fatal("the chat's order changed")
	}
}

// Regression 2. A restart at any point: same id, one send, right recipient.
func TestQueuedSendRestartAtEveryStage(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	home := w.alice.home
	reopen := func(a *Agent) *Agent {
		t.Helper()
		a.Close()
		next, err := Open(home)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { next.Close() })
		return next
	}
	a := w.alice
	a.queued.budget = 100 * time.Millisecond
	free := stallPersonChains(t, w, a)
	accepted := protocol.NewID()
	if _, err := a.QueueSend(ScheduledSendInput{ID: accepted, Conv: conv, Body: "restart while accepted"}, "", 0); err != nil {
		t.Fatal(err)
	}
	dispatching := protocol.NewID()
	if _, err := a.QueueSend(ScheduledSendInput{ID: dispatching, Conv: conv, Body: "restart while dispatching"}, "", 0); err != nil {
		t.Fatal(err)
	}
	a = reopen(a)
	if a.queued.workers != nil {
		t.Fatal("opening alone started work")
	}
	for _, id := range []string{accepted, dispatching} {
		if s := queuedRow(t, a, id); s.Conv != conv || s.Immediate != true {
			t.Fatalf("restart changed %+v", s)
		}
	}
	if _, err := a.store.db.Exec(`UPDATE scheduled_sends SET state='dispatching',rev=rev+1 WHERE id=?`, dispatching); err != nil {
		t.Fatal(err)
	}
	free()
	stop := runAgent(t, a)
	eventually(t, "both resumed", func() bool { return queuedLIDCopies(t, a, accepted) > 0 && queuedLIDCopies(t, a, dispatching) > 0 })
	eventually(t, "each arrived once", func() bool {
		return inboxCount(t, w.bob, `lid = ?`, accepted) == 1 && inboxCount(t, w.bob, `lid = ?`, dispatching) == 1
	})
	stop()
	// Died right after storing, before the record said so: found, not repeated.
	stored := protocol.NewID()
	if _, err := a.QueueSend(ScheduledSendInput{ID: stored, Conv: conv, Body: "restart after the store"}, "", 30*time.Second); err != nil {
		t.Fatal(err)
	}
	copies := queuedLIDCopies(t, a, stored)
	if copies == 0 {
		t.Fatal("not stored")
	}
	if _, err := a.store.db.Exec(`INSERT OR REPLACE INTO scheduled_sends(id,conv,topic,body,reply_to,quote,due_at,timezone,rev,state,audience,files,created_at,updated_at,immediate)
		VALUES(?,?,'',?,'','',1,'UTC',2,'dispatching','{"persons":[],"pids":[]}','[]',1,1,1)`, stored, conv, "restart after the store"); err != nil {
		t.Fatal(err)
	}
	a = reopen(a)
	runAgent(t, a)
	eventually(t, "the record settled", func() bool { _, found, _ := a.ScheduledSend(stored); return !found })
	if got := queuedLIDCopies(t, a, stored); got != copies {
		t.Fatalf("restart stored it again: %d -> %d", copies, got)
	}
	// Nothing an ordinary message does starts work.
	var jobs int
	if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE attempts>0`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("a queued message ran work: %d %v", jobs, err)
	}
}

// Regression 3. The Hub took it and the answer was lost: the same copy is
// retried, the person gets one message, and sending the id again is success.
func TestQueuedSendAmbiguousAcceptReconciles(t *testing.T) {
	w := newWorld(t, "")
	stopBob := runAgent(t, w.bob)
	defer stopBob()
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	base := w.alice.hub.http.Transport
	var dropped sync.Once
	lost := make(chan struct{})
	w.alice.hub.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, err := base.RoundTrip(r)
		if err == nil && r.Method == "POST" && r.URL.Path == "/v1/messages" {
			drop := false
			dropped.Do(func() { drop = true })
			if drop {
				resp.Body.Close()
				close(lost)
				return nil, context.DeadlineExceeded
			}
		}
		return resp, err
	})
	runAgent(t, w.alice)
	id := protocol.NewID()
	in := ScheduledSendInput{ID: id, Conv: conv, Body: "taken, answer lost"}
	if _, err := w.alice.QueueSend(in, "", 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-lost:
	case <-time.After(30 * time.Second):
		t.Fatal("no response was lost")
	}
	eventually(t, "the message arrived", func() bool { return inboxCount(t, w.bob, `lid = ?`, id) == 1 })
	copies := queuedLIDCopies(t, w.alice, id)
	res, err := w.alice.QueueSend(in, "", 5*time.Second)
	if err != nil || res.Row.State != ScheduledSent {
		t.Fatalf("sending the same id again after it went: %+v %v", res, err)
	}
	if queuedLIDCopies(t, w.alice, id) != copies || inboxCount(t, w.bob, `lid = ?`, id) != 1 {
		t.Fatal("a second send")
	}
	eventually(t, "the copies settled", func() bool {
		var open int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE lid=? AND state NOT IN ('custody','delivered')`, id).Scan(&open)
		return open == 0
	})
	if _, err = w.alice.DiscardQueuedSend(id); err == nil || !strings.Contains(err.Error(), "already sent") {
		t.Fatalf("discarding what was sent: %v", err)
	}
}

// Regression 4. A real refusal stops the message: short words, content kept,
// retry still checked, and nothing leaves. A frozen person stays frozen.
func TestQueuedSendTerminalFailsClosedInPlainWords(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	freeze(t, w.alice, w.bob)
	id := protocol.NewID()
	res, err := w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: conv, Body: "to a frozen person"}, "", 30*time.Second)
	if err != nil {
		t.Fatalf("accepting is local: %v", err)
	}
	if res.Row.State != ScheduledNeedsReview || res.AttemptErr == nil {
		t.Fatalf("a frozen person's message: %+v", res)
	}
	s := queuedRow(t, w.alice, id)
	if s.Body != "to a frozen person" || s.Detail == "" {
		t.Fatalf("content or reason lost: %+v", s)
	}
	plainWords(t, s.Detail)
	if s.Diagnostic == "" {
		t.Fatal("no details behind the words")
	}
	if queuedLIDCopies(t, w.alice, id) != 0 {
		t.Fatal("a copy for a frozen person")
	}
	if rows, err := w.alice.QueuedSends(conv); err != nil || len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("not in its chat: %+v %v", rows, err)
	}
	// An explicit retry and a repeated Send both re-decide; neither opens the gate.
	if _, err = w.alice.RetryQueuedSend(id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the retry stopped again", func() bool { return queuedRow(t, w.alice, id).State == ScheduledNeedsReview })
	res, err = w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: conv, Body: "to a frozen person"}, "", 30*time.Second)
	if err != nil || res.Row.State != ScheduledNeedsReview {
		t.Fatalf("a repeated send: %+v %v", res, err)
	}
	if queuedLIDCopies(t, w.alice, id) != 0 {
		t.Fatal("a copy for a frozen person")
	}
	if _, err = w.alice.DiscardQueuedSend(id); err != nil {
		t.Fatal(err)
	}
	if rows, err := w.alice.QueuedSends(conv); err != nil || len(rows) != 0 {
		t.Fatalf("still shown after discard: %+v %v", rows, err)
	}
	// A device with no place in a group is refused before anything is kept.
	if _, err = w.alice.QueueSend(ScheduledSendInput{ID: protocol.NewID(), Conv: strings.Repeat("e", 64), Body: "nowhere"}, "", 0); err == nil {
		t.Fatal("accepted for an unknown conversation")
	}
}

// Regression 5. Cancel is the person's, a timeout never is; retries and repeats
// meet the one record; a cancel during an attempt beats its commit.
func TestQueuedSendCancelRetryAndRepeatMeetOneRecord(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	conv := p.State.Conv
	w.alice.queued.budget = 2 * time.Second
	free := stallPersonChains(t, w, w.alice)
	id := protocol.NewID()
	in := ScheduledSendInput{ID: id, Conv: conv, Body: "one record"}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := w.alice.QueueSend(in, "", 0); err != nil {
				t.Errorf("repeat: %v", err)
			}
		}()
	}
	wg.Wait()
	var rows int
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM scheduled_sends WHERE id=?`, id).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("records for one send: %d %v", rows, err)
	}
	if _, err := w.alice.RetryQueuedSend(id); err != nil { // on its way: only sooner
		t.Fatal(err)
	}
	other := in
	other.Body = "other words under the same id"
	if _, err := w.alice.QueueSend(other, "", 0); !errors.Is(err, ErrScheduledIDConflict) {
		t.Fatalf("different words under one id: %v", err)
	}
	eventually(t, "an attempt in flight", func() bool { return queuedRow(t, w.alice, id).State == ScheduledDispatching })
	if _, err := w.alice.DiscardQueuedSend(id); err != nil {
		t.Fatalf("cancel during an attempt: %v", err)
	}
	if s := queuedRow(t, w.alice, id); s.State != ScheduledCancelled || s.Body != "" {
		t.Fatalf("cancelled as %+v", s)
	}
	free() // the attempt may now reach its commit, which must lose
	eventually(t, "the worker finished", func() bool {
		w.alice.queued.Lock()
		defer w.alice.queued.Unlock()
		_, running := w.alice.queued.workers[conv]
		return !running
	})
	if queuedLIDCopies(t, w.alice, id) != 0 {
		t.Fatal("a cancelled message went out")
	}
	if _, err := w.alice.QueueSend(in, "", 0); !errors.Is(err, ErrQueuedSendDiscarded) {
		t.Fatalf("a late repeat of a discarded message: %v", err)
	}
	if _, err := w.alice.RetryQueuedSend(id); err == nil {
		t.Fatal("retried a discarded message")
	}
}

// A timeout is not a cancel: under the stall the row is still there, ready.
func TestQueuedSendTimeoutNeverCancels(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	w.alice.queued.budget = 50 * time.Millisecond
	free := stallPersonChains(t, w, w.alice)
	id := protocol.NewID()
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: p.State.Conv, Body: "many timeouts"}, "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "failed attempts", func() bool { return queuedRow(t, w.alice, id).Diagnostic != "" })
	time.Sleep(1500 * time.Millisecond) // past a retry, under its bound
	if s := queuedRow(t, w.alice, id); s.State == ScheduledCancelled || s.State == ScheduledNeedsReview || s.Body != "many timeouts" {
		t.Fatalf("a timeout ended the message: %+v", s)
	}
	free()
	eventually(t, "sent in the end", func() bool { return queuedLIDCopies(t, w.alice, id) > 0 })
}

// Regression 6. It keeps the place it took when accepted, through waiting and
// being sent; an inbound message arriving meanwhile stays below it. The wire
// still carries the real send time.
func TestQueuedSendKeepsItsPlace(t *testing.T) {
	w := newWorld(t, "")
	stopBob := runAgent(t, w.bob)
	defer stopBob()
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.alice, w.bob)
	w.alice.queued.budget = 100 * time.Millisecond
	free := stallPersonChains(t, w, w.alice)
	id := protocol.NewID()
	acceptedAt := time.Now()
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: conv, Body: "written first"}, "", 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // a whole second apart: shown times differ
	_, root := rootOf(t, w.alice, conv)
	in := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Body: "arrived later", Conv: conv, Root: root, LID: protocol.NewID(), Origin: envelope.OriginUI})
	if err := w.alice.verifyAndStore(tctx(t), in); err != nil {
		t.Fatal(err)
	}
	free()
	eventually(t, "sent", func() bool { return queuedLIDCopies(t, w.alice, id) > 0 })
	msgs, err := w.alice.MessagingConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range msgs {
		order = append(order, m.Body)
	}
	got := strings.Join(order, " | ")
	if !strings.Contains(got, "written first | arrived later") {
		t.Fatalf("reconciling moved it: %s", got)
	}
	var raw string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? LIMIT 1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err = json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatal(err)
	}
	if env.TS < acceptedAt.Unix() {
		t.Fatalf("wire time %d before acceptance", env.TS)
	}
}

// The incident's path: in a group, a message that waited out a stall stayed below
// what arrived meanwhile. It keeps the place it took when accepted.
func TestQueuedGroupSendKeepsItsPlace(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	conv := p.State.Conv
	w.alice.queued.budget = 100 * time.Millisecond
	free := stallPersonChains(t, w, w.alice)
	id := protocol.NewID()
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: conv, Body: "written first"}, "", 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // a whole second apart: shown times differ
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: "arrived later"}); err != nil {
		t.Fatal(err)
	}
	free()
	eventually(t, "sent", func() bool { return queuedLIDCopies(t, w.alice, id) > 0 })
	eventually(t, "received", func() bool { return w.alice.shows(t, conv, "arrived later") })
	msgs, err := w.alice.MessagingConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, m := range msgs {
		order = append(order, m.Body)
	}
	if got := strings.Join(order, " | "); !strings.Contains(got, "written first | arrived later") {
		t.Fatalf("it moved below what arrived meanwhile: %s", got)
	}
}

// Queued rows are the chat's, never schedules, and the deadline loop leaves them.
func TestQueuedSendIsNotASchedule(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	w.alice.queued.budget = 50 * time.Millisecond
	free := stallPersonChains(t, w, w.alice)
	id := protocol.NewID()
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: p.State.Conv, Body: "not a schedule"}, "", 0); err != nil {
		t.Fatal(err)
	}
	rows, err := w.alice.ScheduledSends(true)
	if err != nil || len(rows) != 0 {
		t.Fatalf("listed as a schedule: %+v %v", rows, err)
	}
	if _, err = w.alice.EditScheduledSend(id, queuedRow(t, w.alice, id).Rev, ScheduledSendInput{Conv: p.State.Conv, Body: "edited", Due: time.Now().Add(time.Hour)}); !errors.Is(err, ErrScheduledStale) {
		t.Fatalf("edited as a schedule: %v", err)
	}
	free()
}

// B7: Cancel after a restart, when the page can no longer put the files back,
// holds the message instead: sending stops (an attempt's commit loses), and
// its body, file pointers and kept bytes stay until the person's Retry or
// Discard. A restart never resumes it; Retry sends exactly those files once.
func TestQueuedSendHoldKeepsFilesAndBeatsAttempt(t *testing.T) {
	w, _, p, stops := groupTurnsFixture(t)
	stops[w.alice]()
	conv := p.State.Conv
	a := w.alice
	a.queued.budget = 2 * time.Second
	path, data := writeFile(t, t.TempDir(), "held.bin", 3000)
	// The same bytes in an earlier message: deleting it must not collect the held copy.
	prior, err := a.SendConv(WithQueuedSend(tctx(t), ""), conv, ConvOutgoing{Body: "earlier file", Files: []OutgoingFile{{Path: path}}})
	if err != nil {
		t.Fatal(err)
	}
	free := stallPersonChains(t, w, a)
	id := protocol.NewID()
	if _, err = a.QueueSend(ScheduledSendInput{ID: id, Conv: conv, Body: "held with a file", Files: []OutgoingFile{{Name: "held.bin", Path: path}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "an attempt in flight", func() bool { return queuedRow(t, a, id).State == ScheduledDispatching })
	held, err := a.HoldQueuedSend(id)
	if err != nil || held.State != ScheduledNeedsReview || held.Body != "held with a file" || len(held.Files) != 1 || held.Files[0].SHA256 != sha(data) {
		t.Fatalf("hold during an attempt: %+v %v", held, err)
	}
	plainWords(t, held.Detail)
	if again, err := a.HoldQueuedSend(id); err != nil || again.State != ScheduledNeedsReview || again.Rev != held.Rev {
		t.Fatalf("a repeated hold: %+v %v", again, err)
	}
	free() // the attempt may now reach its commit, which must lose
	eventually(t, "the worker finished", func() bool {
		a.queued.Lock()
		defer a.queued.Unlock()
		_, running := a.queued.workers[conv]
		return !running
	})
	if queuedLIDCopies(t, a, id) != 0 {
		t.Fatal("a held message went out")
	}
	if _, err = a.Retract(tctx(t), ControlRef{Conv: conv, ID: prior.LID, Fingerprint: a.Self().Fingerprint()}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(a.keptPath(sha(data))); err != nil {
		t.Fatal("held message's kept bytes collected", err)
	}
	a.driveQueuedSends() // what a restart runs
	time.Sleep(300 * time.Millisecond)
	if s := queuedRow(t, a, id); s.State != ScheduledNeedsReview || s.Body != "held with a file" || len(s.Files) != 1 || queuedLIDCopies(t, a, id) != 0 {
		t.Fatalf("restart resumed or emptied a held message: %+v", s)
	}
	if _, err = a.RetryQueuedSend(id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the retry sent it once", func() bool { return queuedLIDCopies(t, a, id) > 0 && queuedRow(t, a, id).State == ScheduledSent })
	var out string
	a.store.db.QueryRow(`SELECT id FROM outbox WHERE conv=? AND lid=? LIMIT 1`, conv, id).Scan(&out)
	r, f, err := a.OpenSentAttachment(tctx(t), out, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(readAll(t, r), data) || f.Name != "held.bin" {
		t.Fatal("held file bytes or name changed")
	}
	if _, err = a.HoldQueuedSend(id); err == nil || !strings.Contains(err.Error(), "already sent") {
		t.Fatalf("holding a sent message: %v", err)
	}
}
