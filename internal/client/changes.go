package client

import "sync"

// Changes let an in-process view, such as the daemon's messenger page, learn
// that this installation's local state changed without polling. The daemon's
// own writes signal through the store; writes made by other agentnet
// processes arrive as a wake on the local socket (kick.go), which signals
// too. A change says only "look again", never what changed.

type changeFeed struct {
	mu  sync.Mutex
	seq uint64
	ch  chan struct{}
}

func newChangeFeed() *changeFeed { return &changeFeed{ch: make(chan struct{})} }

func (f *changeFeed) bump() {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.seq++
	close(f.ch)
	f.ch = make(chan struct{})
	f.mu.Unlock()
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

// NoteChange records a change made through this Agent that does not pass
// through the store's own write methods (for example a direct update).
func (a *Agent) NoteChange() { a.changes.bump() }

// changed tells the store's owner that local state changed.
func (s *store) changed() {
	if s.onChange != nil {
		s.onChange()
	}
}

// done signals a change when err is nil and returns err.
func (s *store) done(err error) error {
	if err == nil {
		s.changed()
	}
	return err
}
