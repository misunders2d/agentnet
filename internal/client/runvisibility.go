package client

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// Silence is a notice, never a work deadline. Local timers and stored
// progress wake it; this does not poll the Hub or stop the harness.
const runStallNotice = 15 * time.Minute

type runActivity struct {
	last   atomic.Int64
	wake   chan struct{}
	warned bool // accessed only by the run's watcher
}

func (a *runActivity) stallDue(now time.Time) (time.Duration, bool) {
	if a.warned {
		return 0, false
	}
	remaining := runStallNotice - now.Sub(time.Unix(0, a.last.Load()))
	if remaining > 0 {
		return remaining, false
	}
	a.warned = true
	return 0, true
}

func newRunActivity() *runActivity {
	a := &runActivity{wake: make(chan struct{}, 1)}
	a.last.Store(time.Now().UnixNano())
	return a
}

type activityWriter struct {
	io.Writer
	activity *runActivity
}

func (w activityWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if n > 0 {
		w.activity.touch(time.Now())
	}
	return n, err
}

func (a *runActivity) touch(at time.Time) {
	stamp := at.UnixNano()
	for old := a.last.Load(); stamp > old; old = a.last.Load() {
		if a.last.CompareAndSwap(old, stamp) {
			break
		}
	}
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func runContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

func (s *store) markRunVisibility(id string, stalled bool) (bool, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var since int64
	if err := tx.QueryRow(`SELECT last_attempt_at FROM inbox WHERE id=? AND state='running'`, id).Scan(&since); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	detail := "Running since " + time.Unix(since, 0).UTC().Format(time.RFC3339) + ". Stop is available; nothing stops automatically unless you set a time limit."
	if stalled {
		detail = fmt.Sprintf("Seems stuck: no harness output or progress for %d minutes. ", int64(runStallNotice/time.Minute)) + detail
	}
	res, err := tx.Exec(`UPDATE inbox SET detail=?, notified=?, review_sent=0 WHERE id=? AND state='running' AND coalesce(detail,'') != ?`, detail, !stalled, id, detail)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	if n > 0 {
		if _, err := tx.Exec(`DELETE FROM reported WHERE item=?`, id); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if n > 0 {
		s.changed()
	}
	return n > 0, nil
}

func (s *store) busyDetail(id string) (string, error) {
	var since int64
	err := s.db.QueryRow(`SELECT last_attempt_at FROM inbox WHERE state='running' AND id != ? ORDER BY last_attempt_at, id LIMIT 1`, id).Scan(&since)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("waiting: the agent here is busy with another request since %s", time.Unix(since, 0).UTC().Format(time.RFC3339)), nil
}

func (a *Agent) noteBusyQueue() {
	busy, err := a.store.busyDetail("")
	if err != nil {
		a.Logf("queue status: %v", err)
		return
	}
	res, err := a.store.db.Exec(`UPDATE inbox SET status_due=status_due+1, detail=nullif(?, '')
		WHERE state IN ('pending','accepted') AND kind IN ('question','task') AND local=0 AND replica=0
		AND coalesce(detail,'') != ? AND (? != '' OR detail LIKE 'waiting: the agent here is busy%')`, busy, busy, busy)
	if err != nil {
		a.Logf("queue status: %v", err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		a.store.changed()
		a.wakeStatus()
	}
}

func (s *store) latestRunProgress(id string) time.Time {
	var ms int64
	if s.db.QueryRow(`SELECT coalesce(max(created_ms),0) FROM outbox WHERE reply_to=? AND status='progress'`, id).Scan(&ms) != nil || ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}
