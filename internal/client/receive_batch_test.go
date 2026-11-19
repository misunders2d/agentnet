package client

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// B2: a device coming back to a backlog takes it in pages: one transaction
// and one acknowledgement page for many messages, each stored once, in order.
// AGENTNET_BACKLOG_N sizes it (default 1000, 200 under race); BACKLOG_SUMMARY reports the rate.
func TestReceiveBacklogInPages(t *testing.T) {
	n := 1000
	if raceOn {
		n = 200 // still 4+ batches, 2+ pages
	}
	if v, e := strconv.Atoi(os.Getenv("AGENTNET_BACKLOG_N")); e == nil && v > 0 {
		n = v
	}
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	stopBob := runAgent(t, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "opener"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the DM on bob", func() bool {
		var got int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND body=?`, conv, "opener").Scan(&got)
		return got == 1
	})
	stopBob()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = protocol.NewID()
		if _, err := w.alice.QueueSend(ScheduledSendInput{ID: ids[i], Conv: conv, Body: fmt.Sprintf("backlog %d", i)}, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	w.alice.driveQueuedSends()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		var waiting int
		if err := w.alice.store.db.QueryRow(`SELECT (SELECT count(*) FROM scheduled_sends WHERE immediate=1 AND state IN ('pending','dispatching')) + (SELECT count(*) FROM outbox WHERE recipient=? AND state IN ('queued','waiting'))`, w.bob.Address).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d still not at the relay", waiting)
		}
		w.alice.postOutboxBackground()
		time.Sleep(100 * time.Millisecond)
	}
	started := time.Now()
	runAgent(t, w.bob)
	for {
		var got int
		if err := w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND kind=? AND coalesce(sub,'')='' AND body<>'opener'`, conv, "message").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got >= n {
			break
		}
		if time.Since(started) > 5*time.Minute {
			t.Fatalf("%d of %d received", got, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	drained := time.Since(started)
	t.Logf("BACKLOG_SUMMARY n=%d drain=%s (%.0f/s)", n, drained.Round(time.Millisecond), float64(n)/drained.Seconds())
	rows, err := w.bob.store.db.Query(`SELECT lid FROM inbox WHERE conv=? AND kind=? AND coalesce(sub,'')='' AND body<>'opener' ORDER BY rowid`, conv, "message")
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
		t.Fatalf("%d stored for %d sent", len(got), n)
	}
	for i := range ids {
		if got[i] != ids[i] {
			t.Fatalf("order changed at %d", i)
		}
	}
	eventually(t, "every copy acknowledged to the relay", func() bool {
		var unacked int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE conv=? AND acked=0`, conv).Scan(&unacked)
		return unacked == 0
	})
}
