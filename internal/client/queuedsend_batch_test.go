package client

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// B1: a burst accepted in one chat drains in batches: one round of Hub checks,
// one transaction and one change for many messages, in their order.
// AGENTNET_DRAIN_N sizes it (default 1000); DRAIN_SUMMARY reports the rates.
func TestQueuedBurstDrainsInBatches(t *testing.T) {
	n := 1000
	if v, e := strconv.Atoi(os.Getenv("AGENTNET_DRAIN_N")); e == nil && v > 0 {
		n = v
	}
	w, _, group, _ := groupTurnsFixture(t)
	dm := newDM(t, w.alice, w.bob)
	ids := make([]string, n)
	// Accepted while the worker is held, so the drain is timed alone.
	q := &w.alice.queued
	q.Lock()
	q.closed = true
	q.Unlock()
	started := time.Now()
	for i := range ids {
		ids[i] = protocol.NewID()
		if _, err := w.alice.QueueSend(ScheduledSendInput{ID: ids[i], Conv: dm, Body: fmt.Sprintf("burst %d", i)}, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	accepted := time.Since(started)
	q.Lock()
	q.closed = false
	q.Unlock()
	started = time.Now()
	w.alice.driveQueuedSends()
	// The person keeps writing in another chat meanwhile: accepting stays quick.
	var slowest time.Duration
	probes := 0
	deadline := time.Now().Add(4 * time.Minute)
	for {
		var waiting int
		if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM scheduled_sends WHERE immediate=1 AND state IN ('pending','dispatching') AND conv=?`, dm).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d still queued", waiting, n)
		}
		if probes < 10 {
			probes++
			at := time.Now()
			if _, err := w.alice.QueueSend(ScheduledSendInput{ID: protocol.NewID(), Conv: group.State.Conv, Body: "while draining"}, "", 0); err != nil {
				t.Fatal(err)
			}
			slowest = max(slowest, time.Since(at))
		}
		time.Sleep(200 * time.Millisecond)
	}
	drained := time.Since(started)
	t.Logf("DRAIN_SUMMARY n=%d accept=%s (%.0f/s) drain=%s (%.0f/s) slowest_accept_during=%s", n, accepted.Round(time.Millisecond), float64(n)/accepted.Seconds(), drained.Round(time.Millisecond), float64(n)/drained.Seconds(), slowest.Round(time.Millisecond))
	// Each once, in the order accepted.
	rows, err := w.alice.store.db.Query(`SELECT lid FROM outbox WHERE conv=? AND recipient=? AND lid<>'' ORDER BY rowid`, dm, w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var lid string
		if err := rows.Scan(&lid); err != nil {
			t.Fatal(err)
		}
		got = append(got, lid)
	}
	if len(got) != n {
		t.Fatalf("%d copies to bob for %d sends", len(got), n)
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("order changed at %d", i)
		}
	}
}

// A batch stores what one-at-a-time sending stores: the same order, topics,
// topic heads, replies and quotes. (Which of two same-second branches heads a
// topic depends on the second each landed in, so the chain here stays linear.)
func TestQueuedBatchMatchesOneAtATime(t *testing.T) {
	w, _, group, _ := groupTurnsFixture(t)
	dm := newDM(t, w.alice, w.bob)
	type spec struct {
		topic        string // "T": the run's topic; "new"; ""
		reply, quote bool   // to the run's main message, stored before the run
	}
	specs := []spec{{topic: "T"}, {topic: "T"}, {reply: true}, {quote: true}, {topic: "new"}, {}, {topic: "T"}, {topic: "T", quote: true}, {topic: "T"}}
	run := func(conv string, held bool) []string {
		t.Helper()
		main, err := w.alice.SendConv(WithQueuedSend(tctx(t), protocol.NewID()), conv, ConvOutgoing{Body: "main"})
		if err != nil {
			t.Fatal(err)
		}
		seed, err := w.alice.SendConv(WithQueuedSend(tctx(t), protocol.NewID()), conv, ConvOutgoing{Body: "opens the topic", Topic: "new"})
		if err != nil {
			t.Fatal(err)
		}
		var topic string
		if err = w.alice.store.db.QueryRow(`SELECT topic FROM outbox WHERE lid=? LIMIT 1`, seed.LID).Scan(&topic); err != nil {
			t.Fatal(err)
		}
		q := &w.alice.queued
		if held {
			q.Lock()
			q.closed = true
			q.Unlock()
		}
		ids := make([]string, len(specs))
		for i, s := range specs {
			ids[i] = protocol.NewID()
			in := ScheduledSendInput{ID: ids[i], Conv: conv, Body: fmt.Sprintf("message %d", i+1)}
			switch s.topic {
			case "T":
				in.Topic = topic
			case "new":
				in.Topic = "new"
			}
			if s.reply {
				in.ReplyTo = main.LID
			}
			if s.quote {
				in.Quote = main.LID
			}
			if _, err = w.alice.QueueSend(in, "", 0); err != nil {
				t.Fatal(err)
			}
			if !held {
				eventually(t, "message stored", func() bool { return queuedLIDCopies(t, w.alice, ids[i]) > 0 })
			}
		}
		if held {
			q.Lock()
			q.closed = false
			q.Unlock()
			w.alice.driveQueuedSends()
		}
		eventually(t, "every message stored", func() bool { return queuedLIDCopies(t, w.alice, ids[len(ids)-1]) > 0 })
		name := map[string]string{"": "-", main.LID: "main", seed.LID: "seed", topic: "T"}
		for i, id := range ids {
			name[id] = fmt.Sprint(i + 1)
		}
		var out []string
		last := int64(0)
		for i, id := range ids {
			var row int64
			var tp, reply, quote string
			if err = w.alice.store.db.QueryRow(`SELECT min(rowid),coalesce(topic,''),coalesce(reply_to,''),coalesce(quote,'') FROM outbox WHERE lid=?`, id).Scan(&row, &tp, &reply, &quote); err != nil {
				t.Fatalf("message %d: %v", i+1, err)
			}
			if row <= last {
				t.Fatalf("message %d stored out of order", i+1)
			}
			last = row
			if _, ok := name[tp]; !ok {
				name[tp] = "new" + fmt.Sprint(i+1)
			}
			out = append(out, fmt.Sprintf("%d topic=%s reply=%s quote=%s", i+1, name[tp], name[reply], name[quote]))
		}
		return out
	}
	for _, conv := range []string{dm, group.State.Conv} {
		serial, batched := run(conv, false), run(conv, true)
		if !slices.Equal(serial, batched) {
			t.Fatalf("batched differs from one at a time:\n%s\n---\n%s", strings.Join(serial, "\n"), strings.Join(batched, "\n"))
		}
		t.Logf("%s", strings.Join(batched, "; "))
	}
}

// A key change recorded between a batch's checks and its commit stores
// nothing for that recipient: the message is held and prepared again, and then
// refused as any send to a changed key is.
func TestQueuedBatchRechecksKeyAtCommit(t *testing.T) {
	w, _, _, _ := groupTurnsFixture(t)
	dm := newDM(t, w.alice, w.bob)
	q := &w.alice.queued
	q.Lock()
	q.closed = true
	q.Unlock()
	id := protocol.NewID()
	if _, err := w.alice.QueueSend(ScheduledSendInput{ID: id, Conv: dm, Body: "checked, then the key moved"}, "", 0); err != nil {
		t.Fatal(err)
	}
	rows, err := w.alice.nextQueuedSends(dm)
	if err != nil || len(rows) != 1 {
		t.Fatalf("queued: %d %v", len(rows), err)
	}
	b := &sendBatch{}
	ctx := context.WithValue(withRoundTrips(tctx(t)), sendBatchKey{}, b)
	if _, err = w.alice.dispatchScheduledLocked(ctx, id, nil); err != nil || len(b.rows) != 1 {
		t.Fatalf("not staged: %d %v", len(b.rows), err)
	}
	// Another worker meets bob's new key in the directory meanwhile.
	replacement, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.store.setPending(replacement.Public(w.bob.Address)); err != nil {
		t.Fatal(err)
	}
	out, waiting := w.alice.storeSendBatch(b)
	if n := queuedLIDCopies(t, w.alice, id); n != 0 {
		t.Fatalf("%d copies stored for a key marked changed", n)
	}
	if len(out) != 1 || out[0].row.State != ScheduledPending || !waiting {
		t.Fatalf("not held: %+v waiting=%v", out, waiting)
	}
	// Prepared again: the cached directory answer still names the old key,
	// and the pending change refuses it.
	row, err := w.alice.dispatchScheduledLocked(ctx, id, nil)
	var changed *KeyChangedError
	if !errors.As(err, &changed) || row.State != ScheduledNeedsReview || queuedLIDCopies(t, w.alice, id) != 0 {
		t.Fatalf("second attempt: %s %v, %d copies", row.State, err, queuedLIDCopies(t, w.alice, id))
	}
}

// An attempt that runs out while a later message is checked still stores
// the messages checked before it, in order; the rest waits for the next.
func TestQueuedBatchKeepsCheckedPrefixWhenAttemptEnds(t *testing.T) {
	w, _, _, _ := groupTurnsFixture(t)
	dm := newDM(t, w.alice, w.bob)
	q := &w.alice.queued
	q.Lock()
	q.closed = true
	q.Unlock()
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = protocol.NewID()
		if _, err := w.alice.QueueSend(ScheduledSendInput{ID: ids[i], Conv: dm, Body: fmt.Sprintf("ready %d", i+1)}, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := w.alice.nextQueuedSends(dm)
	if err != nil || len(rows) != len(ids) {
		t.Fatalf("queued: %d %v", len(rows), err)
	}
	ctx, cancel := context.WithCancel(withRoundTrips(tctx(t)))
	defer cancel()
	drainStep = func(i int) {
		if i == 20 {
			cancel() // message 21 exhausts the attempt
		}
	}
	defer func() { drainStep = nil }()
	_, waiting := w.alice.drainQueued(ctx, rows)
	if !waiting {
		t.Fatal("the chat does not wait for message 21")
	}
	for i, id := range ids[:20] {
		if queuedLIDCopies(t, w.alice, id) == 0 {
			t.Fatalf("checked message %d dropped with the attempt", i+1)
		}
	}
	if n := queuedLIDCopies(t, w.alice, ids[20]); n != 0 {
		t.Fatalf("message 21 stored %d copies without its checks", n)
	}
	if s := queuedRow(t, w.alice, ids[20]); s.State != ScheduledPending && s.State != ScheduledDispatching {
		t.Fatalf("message 21 %s, want it waiting", s.State)
	}
}
