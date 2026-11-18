package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// B2: a page is stored whole or not at all. Failing before its commit
// stores nothing and the Hub's redelivery stores each turn once; a page
// committed but not acknowledged, pushed again, stores nothing new, queues
// no second alert and asks for its receipts again.
func TestReceivePageReplayStoresOnce(t *testing.T) {
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
	var envs []envelope.Envelope
	for i := range 6 {
		sent, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: fmt.Sprintf("page %d", i)})
		if err != nil {
			t.Fatal(err)
		}
		var raw string
		if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE lid=? AND recipient=?`, sent.LID, w.bob.Address).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var env envelope.Envelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		envs = append(envs, env)
	}
	stored := func() (rows, unacked, alerts int) {
		t.Helper()
		if err := w.bob.store.db.QueryRow(`SELECT count(*), count(*) FILTER (WHERE acked=0) FROM inbox WHERE conv=? AND body LIKE 'page %'`, conv).Scan(&rows, &unacked); err != nil {
			t.Fatal(err)
		}
		w.bob.store.db.QueryRow(`SELECT coalesce(sum(count),0) FROM alerts WHERE conv=?`, conv).Scan(&alerts)
		return
	}
	if _, err := w.bob.store.db.Exec(`INSERT INTO config(k,v) VALUES('alerts','on') ON CONFLICT(k) DO UPDATE SET v='on'`); err != nil {
		t.Fatal(err)
	}
	crash := errors.New("crashed before the commit")
	pageFault = func() error { return crash }
	err := w.bob.receivePage(tctx(t), envs)
	pageFault = nil
	if !errors.Is(err, crash) {
		t.Fatalf("page error %v, want the crash", err)
	}
	if rows, _, _ := stored(); rows != 0 {
		t.Fatalf("%d turns stored by a page that never committed", rows)
	}
	if err := w.bob.receivePage(tctx(t), envs); err != nil {
		t.Fatal(err)
	}
	rows, unacked, alerts := stored()
	if rows != len(envs) || unacked != len(envs) || alerts != len(envs) {
		t.Fatalf("redelivered page stored %d (%d unacknowledged, %d alerts), want %d", rows, unacked, alerts, len(envs))
	}
	if _, err := w.bob.store.db.Exec(`UPDATE inbox SET acked=1 WHERE conv=?`, conv); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.receivePage(tctx(t), envs); err != nil { // committed, its acknowledgement lost
		t.Fatal(err)
	}
	again, unacked, alertsAgain := stored()
	if again != rows || alertsAgain != alerts || unacked != len(envs) {
		t.Fatalf("pushed again: %d turns (was %d), %d alerts (was %d), %d receipts asked again, want %d", again, rows, alertsAgain, alerts, unacked, len(envs))
	}
}
