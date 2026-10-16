package client

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Match only the actual progress statement, never another status/authority write.
const nativeProgressFaultSQL = `UPDATE inbox SET detail=?,status_due=status_due+?
		WHERE id=? AND sender=? AND verified_by=? AND attempts=? AND state='running' AND replica=0
		AND coalesce(conv,'')=? AND coalesce(pid,'')=?
		AND coalesce(detail,'') NOT IN (?,?) AND substr(coalesce(detail,''),1,length(?))!=?
		AND NOT EXISTS(SELECT 1 FROM native_requests n WHERE n.request=inbox.id AND n.attempt=inbox.attempts
		 AND n.state='pending' AND (n.deadline=0 OR n.deadline>?))`

type nativeProgressFault struct {
	mu                  sync.Mutex
	after               bool
	resultError         bool
	failures            int
	events              chan nativeProgressFaultEvent
	attempts, delegated int
	rows                int64
	fired               chan struct{}
}

func (f *nativeProgressFault) counts() (int, int, int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.attempts, f.delegated, f.rows
}

type nativeProgressFaultConnector struct {
	base  driver.Driver
	dsn   string
	fault *nativeProgressFault
}

func (c *nativeProgressFaultConnector) Driver() driver.Driver { return c.base }
func (c *nativeProgressFaultConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn, err := c.base.Open(c.dsn)
	if err != nil {
		return nil, err
	}
	exec, ok := conn.(driver.ExecerContext)
	if !ok {
		conn.Close()
		return nil, errors.New("fixture driver has no contextual execution")
	}
	return &nativeProgressFaultConn{Conn: conn, exec: exec, fault: c.fault}, nil
}

type nativeProgressFaultConn struct {
	driver.Conn
	exec  driver.ExecerContext
	fault *nativeProgressFault
}

func (c *nativeProgressFaultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if query != nativeProgressFaultSQL {
		return c.exec.ExecContext(ctx, query, args)
	}
	f := c.fault
	f.mu.Lock()
	f.attempts++
	attempt, inject, after, resultError := f.attempts, f.attempts <= f.failures, f.after, f.resultError
	f.mu.Unlock()
	if inject && !after {
		if attempt == 1 {
			close(f.fired)
		}
		f.events <- nativeProgressFaultEvent{unknown: true}
		return nil, context.DeadlineExceeded
	}
	res, err := c.exec.ExecContext(ctx, query, args)
	f.mu.Lock()
	f.delegated++
	var affected int64
	if err == nil {
		if n, e := res.RowsAffected(); e == nil {
			f.rows += n
			affected = n
		}
	}
	f.mu.Unlock()
	unknown := err != nil || (inject && after)
	f.events <- nativeProgressFaultEvent{unknown: unknown, affected: affected}
	if inject && after && err == nil {
		if attempt == 1 {
			close(f.fired)
		}
		if resultError {
			return nativeProgressFaultResult{res}, nil
		}
		return nil, context.DeadlineExceeded
	}
	return res, err
}

func nativeProgressFaultStore(t *testing.T, a *Agent, after bool) *nativeProgressFault {
	t.Helper()
	base := a.store.db.Driver()
	if err := a.store.db.Close(); err != nil {
		t.Fatal("closing fixture connection failed")
	}
	f := &nativeProgressFault{after: after, failures: 1, fired: make(chan struct{}), events: make(chan nativeProgressFaultEvent, 16)}
	// The schema/private file already came from openStore. Reuse its actual
	// driver and exact connection settings; no mock store or migration changes.
	dsn := "file:" + filepath.Join(a.home, "agent.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_txlock=immediate"
	a.store.db = sql.OpenDB(&nativeProgressFaultConnector{base: base, dsn: dsn, fault: f})
	a.store.db.SetMaxOpenConns(1)
	var journal string
	var busy, foreign int
	if a.store.db.QueryRow(`PRAGMA journal_mode`).Scan(&journal) != nil || a.store.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busy) != nil || a.store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreign) != nil {
		t.Fatal("fixture database settings lookup failed")
	}
	if journal != "wal" || busy != 5000 || foreign != 1 || a.store.db.Stats().MaxOpenConnections != 1 {
		t.Fatal("fixture database settings changed")
	}
	return f
}

type nativeProgressAuthority struct {
	request, sender, key, state, conv, pid string
	attempt, replica                       int
}

type nativeProgressFaultRow struct {
	authority nativeProgressAuthority
	detail    string
	due       int
}

func nativeProgressFaultSnapshot(t *testing.T, a *Agent, j job) nativeProgressFaultRow {
	t.Helper()
	return nativeProgressSnapshot(t, a.store, j)
}

func nativeProgressSnapshot(t *testing.T, s *store, j job) nativeProgressFaultRow {
	t.Helper()
	var r nativeProgressFaultRow
	k := &r.authority
	if err := s.db.QueryRow(`SELECT id,sender,coalesce(verified_by,''),attempts,state,coalesce(conv,''),coalesce(pid,''),replica,coalesce(detail,''),status_due FROM inbox WHERE id=?`, j.ID).Scan(&k.request, &k.sender, &k.key, &k.attempt, &k.state, &k.conv, &k.pid, &k.replica, &r.detail, &r.due); err != nil {
		t.Fatal("fixture metadata snapshot failed")
	}
	return r
}

// The bounded re-emission records acceptance separately from final persistence.
// It never infers acceptance or rollback from the stored text after an error.
func nativeProgressPersisted(t *testing.T, s *store, j job, p nativeProgress) {
	t.Helper()
	before := nativeProgressSnapshot(t, s, j)
	accepted, unknown := 0, 0
	for emission := 0; emission < 2; emission++ {
		prior := nativeProgressSnapshot(t, s, j)
		changed, err := s.markNativeProgressOutcome(t.Context(), j, p)
		if err != nil {
			unknown++
			if changed {
				t.Fatal("unknown outcome was accepted")
			}
			continue
		}
		if changed {
			accepted++
		} else if nativeProgressSnapshot(t, s, j) != prior {
			t.Fatal("known unchanged outcome mutated exact row")
		}
		break
	}
	after := nativeProgressSnapshot(t, s, j)
	persisted := after.detail == p.label()+"\nReported at "+p.at.UTC().Format(time.RFC3339)
	t.Logf("progress metadata: accepted=%d unknown=%d persisted=%t due_delta=%d authority_same=%t", accepted, unknown, persisted, after.due-before.due, after.authority == before.authority)
	if !persisted || after.due-before.due != 1 || after.authority != before.authority || accepted > 1 {
		t.Fatal("bounded progress persistence contract failed")
	}
}

// This result makes the real step's RowsAffected outcome unknown to the caller.
type nativeProgressFaultResult struct{ driver.Result }

func (nativeProgressFaultResult) RowsAffected() (int64, error) { return 0, context.DeadlineExceeded }

type nativeProgressFaultEvent struct {
	unknown  bool
	affected int64
}

func testNativeProgressAfterStepError(t *testing.T) {
	t.Run("explicit-outcome", func(t *testing.T) {
		a, j, _, _ := activityDeliveryFixture(t)
		f := nativeProgressFaultStore(t, a, true)
		before := nativeProgressFaultSnapshot(t, a, j)
		p := nativeProgress{kind: activityReading, at: time.Now()}
		changed, err := a.store.markNativeProgressOutcome(t.Context(), j, p)
		if changed || err == nil {
			t.Fatal("after-step error was not an unknown outcome")
		}
		persisted := nativeProgressFaultSnapshot(t, a, j)
		if persisted.due-before.due != 1 || persisted.authority != before.authority || persisted.detail != p.label()+"\nReported at "+p.at.UTC().Format(time.RFC3339) {
			t.Fatal("unknown applied outcome lost exact persistence")
		}
		changed, err = a.store.markNativeProgressOutcome(t.Context(), j, p)
		if changed || err != nil || nativeProgressFaultSnapshot(t, a, j) != persisted {
			t.Fatal("identical retry was promoted to accepted or mutated the row")
		}
		attempts, delegated, rows := f.counts()
		if attempts != 2 || delegated != 2 || rows != 1 {
			t.Fatal("identical retry applied again")
		}
		t.Log("after-step metadata: accepted=0 unknown=1 persisted=1 identical_retry=unchanged")
	})
	testNativeProgressRetry(t, true, 1, false, false)
}
func testNativeProgressBeforeStepOmission(t *testing.T) {
	testNativeProgressRetry(t, false, 1, false, false)
}
func testNativeProgressRepeatedUnknown(t *testing.T) {
	testNativeProgressRetry(t, false, 2, false, false)
}
func testNativeProgressLatestWins(t *testing.T) { testNativeProgressRetry(t, false, 2, true, false) }
func testNativeProgressRowsAffectedError(t *testing.T) {
	testNativeProgressRetry(t, true, 1, false, true)
}

func testNativeProgressRetry(t *testing.T, afterStep bool, failures int, latestWins, resultError bool) {
	a, j, h, _ := activityDeliveryFixture(t)
	f := nativeProgressFaultStore(t, a, afterStep)
	f.failures, f.resultError = failures, resultError
	before := nativeProgressFaultSnapshot(t, a, j)
	a.statusLive.Store(true) // Observe wakeStatus without starting a sender/socket.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	activity := newRunActivity()
	checked, done := make(chan struct{}, 1), make(chan struct{})
	go func() {
		defer close(done)
		a.watchRunActivity(ctx, ctx, j, activity, func() bool {
			select {
			case checked <- struct{}{}:
			default:
			}
			return true
		})
	}()
	defer func() { cancel(); <-done; a.statusLive.Store(false) }()
	barrier := func() {
		t.Helper()
		select {
		case <-checked:
		case <-ctx.Done():
			t.Fatal("watcher barrier expired")
		}
	}
	barrier()
	_, changed := a.Changed()
	next := func() nativeProgressFaultEvent {
		t.Helper()
		select {
		case event := <-f.events:
			return event
		case <-ctx.Done():
			t.Fatal("timer retry did not execute")
		}
		return nativeProgressFaultEvent{}
	}
	p := nativeProgress{kind: activityReading, at: time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)}
	activity.reportProgress(p)
	first := next()
	a.wakeWorker()
	barrier() // The first flush has returned, even though it issued no changed wake.
	mid := nativeProgressFaultSnapshot(t, a, j)
	wantFirst := int64(0)
	if afterStep {
		wantFirst = 1
	}
	if !first.unknown || first.affected != wantFirst || mid.due-before.due != int(wantFirst) || mid.authority != before.authority {
		t.Fatal("unknown step metadata differs from controlled outcome")
	}
	select {
	case <-changed:
		t.Fatal("unknown outcome issued changed wake")
	default:
	}
	if len(a.statusWake) != 0 {
		t.Fatal("unknown outcome issued status wake")
	}
	if latestWins {
		p = nativeProgress{kind: activityEditing, at: p.at.Add(time.Second)}
		activity.reportProgress(p) // New distinct key supersedes the failed value before its timer.
	}
	second := next()
	if second.unknown != (failures > 1) {
		t.Fatal("retry outcome classification changed")
	}
	if latestWins {
		third := next() // The newer value receives its own single retry.
		if third.unknown || third.affected != 1 {
			t.Fatal("latest value lost its independent retry")
		}
	} else {
		// Observe another full interval: neither a second unknown nor an unchanged
		// result may arm another retry. Raw output also cannot bypass that cap.
		w := activityWriter{Writer: io.Discard, activity: activity}
		w.Write([]byte("PRIVATE output is not another progress report"))
		observation := time.NewTimer(nativeActivityInterval + 50*time.Millisecond)
		defer observation.Stop()
		select {
		case <-f.events:
			t.Fatal("progress exceeded one timer retry")
		case <-observation.C:
		case <-ctx.Done():
			t.Fatal("retry-cap observation expired")
		}
	}
	cancel()
	<-done
	final := nativeProgressFaultSnapshot(t, a, j)
	attempts, delegated, rows := f.counts()
	wantAttempts, wantWrites := 2, 1
	if failures > 1 && !latestWins {
		wantWrites = 0
	}
	if latestWins {
		wantAttempts = 3
	}
	if attempts != wantAttempts || rows != int64(wantWrites) || final.due-before.due != wantWrites || final.authority != before.authority {
		t.Fatal("capped retry changed persistence or authority")
	}
	if wantWrites == 1 && final.detail != p.label()+"\nReported at "+p.at.UTC().Format(time.RFC3339) {
		t.Fatal("retry did not persist exact latest label/time")
	}
	if wantWrites == 0 && final != before {
		t.Fatal("omitted repeated unknown mutated row")
	}
	wantWake := !afterStep && wantWrites == 1
	if (len(a.statusWake) == 1) != wantWake || len(h.posted) != 0 {
		t.Fatal("retry changed optional status/output behavior")
	}
	t.Logf("retry metadata: attempts=%d delegated=%d persisted=%d due_delta=%d latest=%t status_wake=%t", attempts, delegated, rows, final.due-before.due, latestWins, len(a.statusWake) == 1)
}

func testNativeProgressUnchangedGuards(t *testing.T) {
	for _, guard := range []string{"duplicate", "wait", "terminal", "stale"} {
		t.Run(guard, func(t *testing.T) {
			a, j, h, _ := activityDeliveryFixture(t)
			p := nativeProgress{kind: activityReading, at: time.Now()}
			switch guard {
			case "duplicate":
				if changed, err := a.store.markNativeProgressOutcome(t.Context(), j, p); !changed || err != nil {
					t.Fatal("duplicate setup failed")
				}
			case "wait":
				if _, err := a.store.db.Exec(`UPDATE inbox SET detail=? WHERE id=?`, roomWaitDetail, j.ID); err != nil {
					t.Fatal(err)
				}
			case "terminal":
				if _, err := a.store.db.Exec(`UPDATE inbox SET state='answered' WHERE id=?`, j.ID); err != nil {
					t.Fatal(err)
				}
			case "stale":
				j.nativeAttempt++
			}
			before := nativeProgressFaultSnapshot(t, a, j)
			f := nativeProgressFaultStore(t, a, false)
			f.failures = 0
			a.statusLive.Store(true)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			activity := newRunActivity()
			done := make(chan struct{})
			go func() { defer close(done); a.watchRunActivity(ctx, ctx, j, activity, func() bool { return true }) }()
			defer func() { cancel(); <-done; a.statusLive.Store(false) }()
			activity.reportProgress(p)
			select {
			case event := <-f.events:
				if event.unknown || event.affected != 0 {
					t.Fatal("guard was not known unchanged")
				}
			case <-ctx.Done():
				t.Fatal("guard statement did not execute")
			}
			activity.touch(time.Now()) // Same latest key cannot cause a retry on another wake.
			observation := time.NewTimer(nativeActivityInterval + 50*time.Millisecond)
			defer observation.Stop()
			select {
			case <-f.events:
				t.Fatal("known unchanged guard retried")
			case <-observation.C:
			case <-ctx.Done():
				t.Fatal("guard no-retry observation expired")
			}
			cancel()
			<-done
			attempts, _, rows := f.counts()
			if attempts != 1 || rows != 0 || nativeProgressFaultSnapshot(t, a, j) != before || len(a.statusWake) != 0 || len(h.posted) != 0 {
				t.Fatal("guard mutated persistence/output")
			}
		})
	}
}
