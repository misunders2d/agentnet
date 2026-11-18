package client

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// S6a: a backlog of acknowledgements, all stalled at the Hub, does not hold
// a live post behind it, and the backlog still drains afterwards.
func TestReceiptBacklogDoesNotDelayLivePost(t *testing.T) {
	w, conv := queuedDMWorld(t)
	for range 200 {
		if _, err := w.alice.store.db.Exec(`INSERT INTO history_receipts(id,acked) VALUES(?,0)`, protocol.NewID()); err != nil {
			t.Fatal(err)
		}
	}
	release := make(chan struct{})
	var once sync.Once
	free := func() { once.Do(func() { w.hub.SetHold(nil); close(release) }) }
	t.Cleanup(free)
	var held atomic.Int32
	w.hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == w.alice.Address && strings.HasSuffix(r.URL.Path, "/ack") {
			held.Add(1)
			<-release
		}
	})
	w.alice.postReceiptsBackground()
	eventually(t, "acknowledgements held at the Hub", func() bool { return held.Load() > 0 })
	before := len(w.hub.Requests())
	started := time.Now()
	queuedTurn(t, w.alice, conv, "live while acks wait")
	for posted := false; !posted; {
		if time.Since(started) > 5*time.Second {
			t.Fatalf("the live post waited behind %d stalled acknowledgements", held.Load())
		}
		for _, r := range w.hub.Requests()[before:] {
			posted = posted || r == w.alice.Address+" POST /v1/messages"
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := held.Load(); n > receiptLanes {
		t.Fatalf("%d acknowledgements in flight, want at most %d", n, receiptLanes)
	}
	free()
	eventually(t, "the backlog acknowledged", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM history_receipts WHERE acked=0`).Scan(&n)
		return n == 0
	})
}
