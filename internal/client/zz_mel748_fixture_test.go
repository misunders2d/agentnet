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
	// with no cost between them also mean no page is still being written. Dropping the wake
	// each time shows a stop however the timers fall; the last stop's wake is left set.
	restIsHeld := func() {
		t.Helper()
		w := &a.convWork
		cost, stopped, finished := w.historyCost.Load(), 0, 0
		dropPace(a)
		for deadline := time.Now().Add(time.Minute); ; time.Sleep(20 * time.Millisecond) {
			armed := paceArmed(a) // before the cost, which a running turn records first
			if now := w.historyCost.Load(); now != cost {
				cost, stopped = now, 0
				finished++
				dropPace(a)
			} else if armed {
				if stopped++; stopped == 2 {
					t.Logf("a rest held: two turns stopped at it, after %d finished", finished)
					return
				}
				dropPace(a)
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
	// Only a firing timer clears a set wake (paceFired). Stream events wake a sync too and
	// finish the copying (members.go), so the outcome cannot show a wake that never fired.
	// Whether that wake then reaches the sync: TestPacedWakeReachesOnlyTheCurrentRun.
	wakeFires := func(what string) {
		t.Helper()
		for limit := time.Now().Add(20 * time.Second); paceArmed(a); time.Sleep(time.Millisecond) {
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
	// A real end and restart of the daemon. A wake set in the ended run, an hour off so it is
	// surely still set, stops with it, and its call arriving anyway reaches neither run.
	dropPace(a)
	a.kickAfter(time.Hour)
	old := paceSerialOf(a)
	if !paceArmed(a) {
		t.Fatal("no paced wake to end with the run")
	}
	stop()
	release()
	if paceArmed(a) {
		t.Fatal("an ended daemon kept its paced wake")
	}
	runAgent(t, a)
	finishes("after a restart during a rest", last)
	var wakes atomic.Int32 // the next run is now syncing, so this is its own kick
	a.kickMu.Lock()
	next := a.kick
	a.kick = func() { wakes.Add(1); next() }
	a.kickMu.Unlock()
	a.paceFired(old)
	if wakes.Load() != 0 {
		t.Fatal("an ended daemon's wake reached the next run")
	}
}

func testRetryReaderOwnership(t *testing.T) {
	for _, mode := range []string{"retry-after-claim", "retry-before-clear", "retry-reconnect", "reader-budget", "reader-person"} {
		t.Run(mode, func(t *testing.T) {
			a := &Agent{}
			a.sendRetry.runs(true)
			defer a.sendRetry.runs(false)
			var wakes atomic.Int32
			a.kick = func() { wakes.Add(1) }
			claimed, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var unblock sync.Once
			releaseClaim := func() { unblock.Do(func() { close(release) }) }
			defer releaseClaim()
			barrier := func(serial uint64) {
				if serial == 1 {
					close(claimed)
					<-release
				}
			}
			armRetry := func() (uint64, *time.Timer) {
				a.retrySendSoon(&HubError{Status: 503})
				a.sendRetry.Lock()
				defer a.sendRetry.Unlock()
				if a.sendRetry.timer == nil {
					t.Fatal("retry was not armed")
				}
				a.sendRetry.timer.Stop() // dispatch is driven below, not by the clock
				return a.sendRetry.timerSerial, a.sendRetry.timer
			}
			person := readerAsks
			if mode == "reader-person" {
				person = "synthetic-person"
			}
			armReader := func() uint64 {
				serial := a.retryReader(person)
				a.sendRetry.Lock()
				defer a.sendRetry.Unlock()
				wait := a.sendRetry.readers[person]
				if wait.timer == nil {
					t.Fatal("reader retry was not armed")
				}
				wait.timer.Stop()
				return serial
			}
			reader := mode == "reader-budget" || mode == "reader-person"
			var serial uint64
			if reader {
				a.readerFiring = func(_ string, serial uint64) { barrier(serial) }
				serial = armReader()
			} else {
				if mode == "retry-after-claim" || mode == "retry-reconnect" {
					a.retryClaimed = barrier
				} else {
					a.retryFiring = barrier
				}
				serial, _ = armRetry()
			}
			go func() {
				defer close(done)
				if reader {
					a.readerFired(person, serial)
				} else {
					a.retryFired(serial)
				}
			}()
			select {
			case <-claimed:
			case <-time.After(5 * time.Second):
				t.Fatal("ownership barrier was not reached")
			}
			if mode != "retry-reconnect" {
				a.sendRetry.runs(false)
				a.sendRetry.runs(true)
			}
			a.kickMu.Lock()
			a.kick = func() { wakes.Add(1) }
			a.kickMu.Unlock()
			a.convWork.take()
			var replacement uint64
			var timer *time.Timer
			if reader {
				replacement = armReader()
			} else {
				replacement, timer = armRetry()
			}
			releaseClaim()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("old callback did not return")
			}
			a.sendRetry.Lock()
			kept := a.sendRetry.timer == timer
			if reader {
				kept = a.sendRetry.readers[person].serial == replacement && a.sendRetry.readers[person].timer != nil
			}
			a.sendRetry.Unlock()
			bits := a.convWork.take()
			want := int32(0)
			if mode == "retry-reconnect" {
				want = 1
			}
			if !kept || bits != 0 || wakes.Load() != want {
				t.Fatalf("old callback crossed run: replacement_kept=%t due=%d wakes=%d", kept, bits, wakes.Load())
			}
			if reader {
				a.readerFired(person, replacement)
			} else {
				a.retryFired(replacement)
			}
			if wakes.Load() != want+1 {
				t.Fatal("owned callback lost delivery")
			}
			if reader && a.convWork.take() != convPersons|convRetry {
				t.Fatal("owned reader callback lost due work")
			}
		})
	}
	t.Run("reader-independent", func(t *testing.T) {
		a := &Agent{}
		a.runTimers(true)
		defer a.runTimers(false)
		var original, current atomic.Int32
		a.kick = func() { original.Add(1) }
		arm := func(person string) uint64 {
			t.Helper()
			serial := a.retryReader(person)
			a.sendRetry.Lock()
			defer a.sendRetry.Unlock()
			wait := a.sendRetry.readers[person]
			if wait.timer == nil {
				t.Fatal("independent reader timer missing")
			}
			wait.timer.Stop()
			return serial
		}
		budget, first, second := arm(readerAsks), arm("first"), arm("second")
		if !a.chainAnswered("first") {
			t.Fatal("answer did not retire the first person's wait")
		}
		replacement := arm("first")
		a.kickMu.Lock()
		a.kick = func() { current.Add(1) }
		a.kickMu.Unlock()
		a.readerFired("first", first)
		if a.convWork.take() != 0 || current.Load() != 0 {
			t.Fatal("answered reader callback mutated replacement work")
		}
		for _, item := range []struct {
			person string
			serial uint64
		}{{readerAsks, budget}, {"second", second}, {"first", replacement}} {
			a.readerFired(item.person, item.serial)
			if a.convWork.take() != convPersons|convRetry {
				t.Fatal("independent reader lost its own due work")
			}
		}
		if original.Load() != 0 || current.Load() != 3 {
			t.Fatal("reader retry did not reach the same run's current stream")
		}
		a.readerFired(readerAsks, budget)
		if a.convWork.take() != 0 || current.Load() != 3 {
			t.Fatal("one reader callback delivered twice")
		}
		arm("retired-at-run-end")
		a.runTimers(false)
		a.runTimers(true)
		a.kickMu.Lock()
		retainedCallback := a.kick != nil
		a.kickMu.Unlock()
		a.sendRetry.Lock()
		retainedWaits := len(a.sendRetry.readers)
		a.sendRetry.Unlock()
		if retainedCallback || retainedWaits != 0 || a.chainWaiting("retired-at-run-end") {
			t.Fatal("new run retained the old stream or old reader waits")
		}
	})
}

func paceArmed(a *Agent) bool {
	a.sendRetry.Lock()
	defer a.sendRetry.Unlock()
	return a.sendRetry.pace != nil
}

func paceSerialOf(a *Agent) uint64 {
	a.sendRetry.Lock()
	defer a.sendRetry.Unlock()
	return a.sendRetry.paceSerial
}

func dropPace(a *Agent) {
	a.sendRetry.Lock()
	defer a.sendRetry.Unlock()
	if a.sendRetry.pace != nil {
		a.sendRetry.pace.Stop()
		a.sendRetry.pace = nil
	}
}

// A paced wake is a timer's call to the current run's sync, and it carries no work: the bits
// in convWork say what a sync does. There is no stream here, whose events wake a sync too, so
// each wake can only be the timer's. A run's kick is the test's own, as streamOnce sets one.
// A wake an hour off is surely still set, so nothing here depends on how the timers fall; the
// journey above ends and restarts a real daemon.
func TestPacedWakeReachesOnlyTheCurrentRun(t *testing.T) {
	t.Run("retry-reader-ownership", testRetryReaderOwnership)
	t.Run("after-claim", func(t *testing.T) {
		for _, transition := range []string{"restart", "reconnect", "close"} {
			t.Run(transition, func(t *testing.T) {
				a := &Agent{}
				defer a.sendRetry.runs(false)
				var old, next atomic.Int32
				var unfenced atomic.Bool
				a.kick = func() { old.Add(1) }
				a.sendRetry.runs(true)
				a.kickAfter(time.Hour)
				serial := paceSerialOf(a)
				claimed, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var unblock sync.Once
				releaseClaim := func() { unblock.Do(func() { close(release) }) }
				defer releaseClaim()
				var once sync.Once
				a.paceClaimed = func() { once.Do(func() { close(claimed); <-release }) }
				go func() { defer close(done); a.paceFired(serial) }()
				select {
				case <-claimed:
				case <-time.After(5 * time.Second):
					t.Fatal("paced callback never reached after-claim barrier")
				}
				if transition == "restart" {
					a.sendRetry.runs(false)
					a.sendRetry.runs(true)
				} else if transition == "close" {
					a.stopBackgroundPosts()
				}
				a.kickMu.Lock()
				a.kick = func() {
					if a.sendRetry.TryLock() {
						unfenced.Store(true)
						a.sendRetry.Unlock()
					}
					if a.kickMu.TryLock() {
						unfenced.Store(true)
						a.kickMu.Unlock()
					}
					next.Add(1)
				}
				a.kickMu.Unlock()
				if transition == "restart" {
					a.kickAfter(time.Hour)
				}
				releaseClaim()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("claimed paced callback did not finish")
				}
				want := int32(0)
				if transition == "reconnect" {
					want = 1
				}
				if old.Load() != 0 || next.Load() != want {
					t.Fatalf("after-claim %s: old stream wakes=%d replacement wakes=%d want=%d", transition, old.Load(), next.Load(), want)
				}
				if unfenced.Load() {
					t.Fatal("paced wake delivery was outside its run or stream lock")
				}
				if transition == "restart" {
					if !paceArmed(a) {
						t.Fatal("old callback consumed replacement run's timer")
					}
					a.paceFired(paceSerialOf(a))
					if next.Load() != 1 {
						t.Fatal("replacement run's own paced callback did not wake it")
					}
				}
			})
		}
	})
	const settle = 100 * time.Millisecond // nothing more may come; a late one could only pass
	a := &Agent{}
	kickOf := func() *atomic.Int32 {
		wakes := new(atomic.Int32)
		a.kickMu.Lock()
		defer a.kickMu.Unlock()
		a.kick = func() { wakes.Add(1) }
		return wakes
	}
	arm := func(d time.Duration) uint64 {
		t.Helper()
		before := paceSerialOf(a)
		a.kickAfter(d)
		after := paceSerialOf(a)
		if after == before {
			t.Fatal("a paced wake was not armed")
		}
		return after
	}
	arrives := func(wakes *atomic.Int32, failure string) {
		t.Helper()
		for limit := time.Now().Add(5 * time.Second); wakes.Load() == 0; time.Sleep(time.Millisecond) {
			if time.Now().After(limit) {
				t.Fatal(failure)
			}
		}
	}
	wakesOnce := func(wakes *atomic.Int32) {
		t.Helper()
		for limit := time.Now().Add(5 * time.Second); paceArmed(a); time.Sleep(time.Millisecond) {
			if time.Now().After(limit) {
				t.Fatal("a paced wake never fired")
			}
		}
		arrives(wakes, "a paced wake fired and woke nothing")
		time.Sleep(settle)
		if n := wakes.Load(); n != 1 {
			t.Fatalf("a paced wake woke the sync %d times", n)
		}
	}
	first := kickOf()
	a.sendRetry.runs(true)
	arm(time.Millisecond)
	wakesOnce(first)

	// Only the earliest wake stays set, whichever was asked first.
	long := arm(time.Hour)
	a.kickAfter(2 * time.Hour)
	if paceSerialOf(a) != long {
		t.Fatal("a later wake replaced an earlier one")
	}
	short := kickOf()
	a.kickAfter(time.Millisecond)
	arrives(short, "a shorter wake waited behind a longer one")
	wakesOnce(short)
	a.paceFired(long)
	if short.Load() != 1 {
		t.Fatal("a replaced wake still woke the sync")
	}

	// The order a sync reaches: new evidence defers a restart by up to a minute, then the
	// same sync's history turn asks for its short rest.
	w := &a.convWork
	w.historyCost.Store(int64(time.Minute))
	now := time.Now()
	w.sweepRestarted = now
	turn := kickOf()
	if a.restartDeferredHistory(now) || !paceArmed(a) {
		t.Fatal("the restart was not deferred behind a wake")
	}
	a.kickAfter(w.historyRest(30*time.Millisecond, a.pacedSync()))
	arrives(turn, "a turn's rest waited behind a deferred restart")

	// A stream replaced within one run: the wake is the run's, and reaches its current stream.
	dropPace(a)
	armedIn := kickOf()
	inFlight := arm(time.Hour)
	current := kickOf()
	a.paceFired(inFlight)
	if armedIn.Load() != 0 {
		t.Fatal("a stale wake reached the stream it was armed in")
	}
	if current.Load() != 1 {
		t.Fatal("a wake did not reach the run's current stream")
	}

	// An ended run: its wake stops, and its call arriving anyway reaches neither run.
	ended := kickOf()
	gone := arm(time.Hour)
	a.sendRetry.runs(false)
	if paceArmed(a) {
		t.Fatal("an ended run kept its wake")
	}
	a.paceFired(gone)
	if ended.Load() != 0 {
		t.Fatal("an ended run's wake reached the sync")
	}
	a.kickAfter(time.Hour)
	if paceArmed(a) {
		t.Fatal("a wake was set with no run")
	}
	a.sendRetry.runs(true)
	next := kickOf()
	a.paceFired(gone)
	if next.Load() != 0 {
		t.Fatal("an old run's wake reached the next run")
	}
	arm(time.Hour) // nor does it use up the wake that is set now
	a.paceFired(gone)
	if next.Load() != 0 || !paceArmed(a) {
		t.Fatal("an old run's wake took the place of the next run's")
	}
	dropPace(a)
	arm(time.Millisecond) // and hides nothing of the next run's
	wakesOnce(next)
	timed := kickOf()
	arm(5 * time.Millisecond) // a real timer across a real end: earlier, it could only wake its own run
	a.sendRetry.runs(false)
	a.sendRetry.runs(true)
	afterIt := kickOf()
	time.Sleep(settle)
	if timed.Load() > 1 || afterIt.Load() != 0 {
		t.Fatal("an old run's timer reached the next run")
	}

	stopped := kickOf()
	last := arm(time.Hour)
	a.stopBackgroundPosts()
	if paceArmed(a) {
		t.Fatal("a stopped agent kept its wake")
	}
	a.paceFired(last)
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
