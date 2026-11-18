package client

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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
