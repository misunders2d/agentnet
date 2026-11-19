package client

import (
	"context"
	"sync"
	"time"
)

// Changes let an in-process view, such as the daemon's messenger page, learn
// that this installation's local state changed without polling. The daemon's
// own writes signal through the store; writes made by other agentnet
// processes arrive as a wake on the local socket (kick.go), which signals
// too. A change says only "look again", never what changed.

type changeFeed struct {
	mu                        sync.Mutex
	seq                       uint64
	ch                        chan struct{}
	content, typing, presence uint64
	unscoped                  uint64
}

// ViewChange keeps independent revision fences: coalescing a typing pulse after
// a stored message must never hide that message's invalidation. Unscoped is the
// last content change no view_dirty key names: state outside the store, or a
// bump with no table write. A view keyed by scope reads again on it.
type ViewChange struct{ Seq, Content, Typing, Presence, Unscoped uint64 }

func newChangeFeed() *changeFeed { return &changeFeed{ch: make(chan struct{})} }

func (f *changeFeed) bump() {
	f.bumpKind("")
}

// bumpStored is the store's own signal after a committed write: its triggers
// named the write's scope in view_dirty, so it is not unscoped.
func (f *changeFeed) bumpStored() {
	f.bumpKind("stored")
}

func (f *changeFeed) bumpKind(kind string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.seq++
	switch kind {
	case "typing":
		f.typing = f.seq
	case "presence":
		f.presence = f.seq
	case "stored":
		f.content = f.seq
	default:
		f.content, f.unscoped = f.seq, f.seq
	}
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
}

func (f *changeFeed) view() (ViewChange, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return ViewChange{f.seq, f.content, f.typing, f.presence, f.unscoped}, f.ch
}

func (f *changeFeed) current() (uint64, <-chan struct{}) {
	if f == nil {
		return 0, nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq, f.ch
}

// Changed returns the current change counter and a channel that is closed
// at the next change.
func (a *Agent) Changed() (uint64, <-chan struct{}) { return a.changes.current() }

// ViewChanges refines Changed for local presentation only. Execution and
// authority continue to use the same store and native admission checks.
func (a *Agent) ViewChanges() (ViewChange, <-chan struct{}) { return a.changes.view() }

// NoteChange records a change made through this Agent that does not pass
// through the store's own write methods (for example a direct update).
func (a *Agent) NoteChange() {
	if a.store != nil {
		a.store.continuityDirty.Store(true)
	}
	a.changes.bump()
}

// noteStoredChange is NoteChange for writes this process made through the
// store's writer without its own signal: their view_dirty triggers named
// their scope, so the change stays scoped (GET /events "scope").
func (a *Agent) noteStoredChange() {
	if a.store != nil {
		a.store.continuityDirty.Store(true)
	}
	a.changes.bumpStored()
}

// changed tells the store's owner that local state changed.
func (s *store) changed() {
	if f := s.committed.Load(); f != nil {
		(*f)()
	}
	if s.onChange != nil {
		s.onChange()
	}
}

// rowChanges counts the rows this process has written so far. The store has
// one writer (sqlitedb), so total_changes() on it sees every write through it,
// direct updates included; a new connection restarts the count.
func (s *store) rowChanges() (int64, bool) {
	var n int64
	return n, s.db.DB.QueryRow(`SELECT total_changes()`).Scan(&n) == nil
}

// done signals a change when err is nil and returns err.
func (s *store) done(err error) error {
	if err == nil {
		s.changed()
	}
	return err
}

// A background pass (the job worker's claims and review, reminders) reads
// much of the store, and a burst of sends or receipts changes it many times
// a second; each change woke every such pass again, and the passes took the
// one store connection from the sends themselves. passEvery paces them: the
// first wake after a quiet moment runs at once, further wakes within it run
// once, at its end. Nothing is missed: a pass captures the next change
// before it looks, as before.
//
// Only the churn waits. A wake that a person or a job is waiting on (a job
// stored or finished, a received request, a command from another process:
// wakeWorkerNow) ends the wait at once, as TDLib runs a lone update at once
// and folds only what is already queued; a burst of sends and receipts
// brings none of those.
const passEvery = 100 * time.Millisecond

// pace waits until passEvery after the last pass began, or until urgent
// (nil: never) has a wake; false: ctx ended.
func pace(ctx context.Context, last *time.Time, urgent <-chan struct{}) bool {
	if wait := passEvery - time.Since(*last); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		case <-urgent:
		}
	}
	*last = time.Now()
	return true
}
