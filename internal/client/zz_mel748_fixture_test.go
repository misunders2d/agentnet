// Kept last in discovery order to preserve the client shards' composition.
package client

import (
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/sqlitedb/schematest"
)

// A test about something else may take the rest out of history copying. This one leaves
// the production pace on and makes a rest cover the whole of what it asserts: a message sent
// during it arrives, copying really does wait, and copying then finishes by the run's own
// timer, with no further kick, and after a restart in the middle of a rest as well. The test
// only rolls the rest forward, never past the longest one, so each ends as any rest ends.
// The bound is for liveness, not speed: a race build's turns cost many times a real one's.
// So this proves fairness and liveness across a rest, and nothing about local feedback time.
func TestOwnEndpointMetadataArrivesAtProductionPace(t *testing.T) {
	w := newWorld(t, "")
	a := w.alice
	stop := runAgent(t, a)
	persons(t, a)
	phone := linked(t, a)
	send := func(body string) string {
		t.Helper()
		v, err := a.SendMessage(tctx(t), Outgoing{To: phone.Address, Kind: envelope.KindMessage, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "a message sent during a rest arrives", func() bool { _, err := phone.TopicOf(v.ID); return err == nil })
		return v.ID
	}
	copied := func(id string) bool {
		var author string
		err := a.store.db.QueryRow(`SELECT author_fp FROM device_thread_links WHERE storage='out' AND id=?`, id).Scan(&author)
		return err == nil && author != ""
	}
	// A rest is proved by what only a rest does: a turn stopping at it arms a wake and records
	// no cost, where a turn that runs records its cost before it arms anything. Two in a row
	// with no cost between them also mean no page is still being written. Clearing the flag
	// each time shows a stop however the timers fall; the last stop's flag is left as it is.
	restIsHeld := func() {
		t.Helper()
		w := &a.convWork
		cost, stopped, finished := w.historyCost.Load(), 0, 0
		w.paceTimer.Store(false)
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(20 * time.Millisecond) {
			armed := w.paceTimer.Load() // before the cost, which a running turn records first
			if now := w.historyCost.Load(); now != cost {
				cost, stopped = now, 0
				finished++
				w.paceTimer.Store(false)
			} else if armed {
				if stopped++; stopped == 2 {
					t.Logf("a rest held: two turns stopped at it, after %d finished", finished)
					return
				}
				w.paceTimer.Store(false)
			}
			if time.Now().After(deadline) {
				if finished >= 2 {
					t.Fatal("a history turn ran through the rest") // only one can have begun before it
				}
				t.Fatal("no history turn reached the rest")
			}
			w.due(convHistory)
			a.kickNow()
		}
	}
	holdRest := func() (release func()) {
		t.Helper()
		roll := func() { a.convWork.historyNotBefore.Store(time.Now().Add(historyRestMax).UnixNano()) }
		roll()
		done, stopped := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(stopped)
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-done:
					return
				case <-tick.C:
					roll()
				}
			}
		}()
		var once sync.Once
		release = func() { once.Do(func() { close(done); <-stopped }) }
		t.Cleanup(release) // a Fatal below or in the caller must not leave the rest rolling
		restIsHeld()
		return release
	}
	finishes := func(what string, ids ...string) {
		t.Helper()
		started, cost := time.Now(), a.convWork.historyCost.Load()
		for _, id := range ids {
			for !copied(id) {
				if time.Since(started) > time.Minute {
					// A wake clears its flag when it fires, and no later turn ran.
					if a.convWork.paceTimer.Load() && a.convWork.historyCost.Load() == cost {
						t.Fatalf("a paced wake never fired %s", what)
					}
					t.Fatalf("copying never finished %s", what)
				}
				time.Sleep(50 * time.Millisecond)
			}
		}
		t.Logf("copying finished %s after %s", what, time.Since(started).Round(time.Millisecond))
	}

	release := holdRest()
	ids := []string{send("first"), send("second"), send("third")}
	restIsHeld() // turns have now stopped at the rest with these messages waiting
	for _, id := range ids {
		if copied(id) {
			t.Fatal("copying did not wait out its rest")
		}
	}
	if !a.pacedSync() {
		t.Fatal("not at the production pace")
	}
	release()
	finishes("after a rest", ids...)

	release = holdRest()
	last := send("sent before a restart")
	if copied(last) {
		t.Fatal("copying did not wait out its rest before the restart")
	}
	stop()
	release()
	runAgent(t, a)
	finishes("after a restart during a rest", last)
}

// Remembered persons are kept for the whole process, by this token alone. So stores made
// from a copy, by either fixture, must never look up one another's. A seeded store starts
// with its image's token, which cannot be looked up: nothing is remembered until a row
// that also replaces it.
func TestRememberedPersonsAreNeverSharedBetweenCopiedStores(t *testing.T) {
	schematest.Use()
	t.Cleanup(restoreImage)
	open := func(home string) *store {
		t.Helper()
		_, path := paths(home)
		s, err := openStore(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.db.Close() })
		return s
	}
	var stores []*store
	for range 3 {
		stores = append(stores, open(t.TempDir()))
	}
	for range 2 {
		home := t.TempDir()
		seedFixtureStore(t, home)
		stores = append(stores, open(home))
	}
	before := continuityResolves.Load()
	for i, s := range stores {
		if got, err := logicalPersons(s.db); err != nil || got != nil {
			t.Fatalf("store %d before any continuity row: %v %v", i, got, err)
		}
	}
	if ran := continuityResolves.Load() - before; ran != 0 {
		t.Fatalf("a start token was looked up: %d resolves", ran)
	}
	seen := map[string]int{}
	for i, s := range stores {
		if err := s.setConfig(map[string]string{continuityHeadPrefix + "same-person": "same-head"}); err != nil {
			t.Fatal(err)
		}
		token := continuityToken(t, s.db)
		if other, dup := seen[token]; dup {
			t.Fatalf("stores %d and %d share a token once there is something to remember", other, i)
		}
		seen[token] = i
		before = continuityResolves.Load()
		if _, err := logicalPersons(s.db); err != nil {
			t.Fatal(err)
		}
		if ran := continuityResolves.Load() - before; ran != 1 {
			t.Fatalf("store %d was served another store's persons: %d resolves", i, ran)
		}
	}
}
