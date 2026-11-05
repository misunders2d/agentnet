// Kept last in discovery order to preserve the client shards' composition.
package client

import (
	"sync"
	"sync/atomic"
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
	// Only a firing timer clears this flag (kickAfter). Stream events wake a sync too and
	// finish the copying (members.go), so the outcome cannot show a wake that never fired.
	// Whether that wake then reaches the sync: TestPacedWakeReachesOnlyTheCurrentRun.
	wakeFires := func(what string) {
		t.Helper()
		for limit := time.Now().Add(20 * time.Second); a.convWork.paceTimer.Load(); time.Sleep(time.Millisecond) {
			if time.Now().After(limit) {
				t.Fatalf("a paced wake never fired %s", what)
			}
		}
	}
	finishes := func(what string, ids ...string) {
		t.Helper()
		started := time.Now()
		for _, id := range ids {
			for !copied(id) {
				if time.Since(started) > time.Minute {
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
	wakeFires("after a rest")
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

// A paced wake is a timer's call to the current run's sync, and it carries no work: the bits
// in convWork say what a sync does. There is no stream here, whose events wake a sync too, so
// each wake can only be the timer's. A run's kick is the test's own, as streamOnce sets one.
func TestPacedWakeReachesOnlyTheCurrentRun(t *testing.T) {
	const settle = 100 * time.Millisecond // nothing more may come; a late one could only pass
	a := &Agent{}
	kickOf := func() *atomic.Int32 {
		wakes := new(atomic.Int32)
		a.kickMu.Lock()
		defer a.kickMu.Unlock()
		a.kick = func() { wakes.Add(1) }
		return wakes
	}
	// The flag falls first in the timer's call, so a timer never armed and one that woke nothing
	// are told apart.
	hasFired := func() {
		t.Helper()
		for limit := time.Now().Add(5 * time.Second); a.convWork.paceTimer.Load(); time.Sleep(time.Millisecond) {
			if time.Now().After(limit) {
				t.Fatal("a paced wake never fired")
			}
		}
	}
	wakesOnce := func(wakes *atomic.Int32) {
		t.Helper()
		hasFired()
		for limit := time.Now().Add(5 * time.Second); wakes.Load() == 0; time.Sleep(time.Millisecond) {
			if time.Now().After(limit) {
				t.Fatal("a paced wake fired and woke nothing")
			}
		}
		time.Sleep(settle)
		if n := wakes.Load(); n != 1 {
			t.Fatalf("a paced wake woke the sync %d times", n)
		}
	}

	first := kickOf()
	a.sendRetry.runs(true)
	a.kickAfter(time.Millisecond)
	a.kickAfter(time.Millisecond) // one wake covers the two
	wakesOnce(first)

	// Fired in one run, it reaches the sync of the run current when its call gets there. The
	// lock holds that call after its flag falls, whenever that is, until the next run is set.
	armedIn := kickOf()
	a.sendRetry.Lock()
	a.kickAfter(time.Millisecond)
	hasFired()
	next := kickOf()
	a.sendRetry.Unlock()
	time.Sleep(settle)
	if armedIn.Load() != 0 {
		t.Fatal("a stale wake reached the run it was armed in")
	}
	wakesOnce(next)

	a.sendRetry.runs(false)
	ended := kickOf()
	a.kickAfter(time.Millisecond)
	hasFired()
	time.Sleep(settle)
	if ended.Load() != 0 {
		t.Fatal("an ended run's wake reached the sync")
	}

	a.sendRetry.runs(true)
	a.stopBackgroundPosts()
	stopped := kickOf()
	a.kickAfter(time.Millisecond)
	hasFired()
	time.Sleep(settle)
	if stopped.Load() != 0 {
		t.Fatal("a stopped agent's wake reached the sync")
	}
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
