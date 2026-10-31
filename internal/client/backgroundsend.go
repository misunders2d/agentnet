package client

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type queuedSendKey struct{}
type scheduledDispatchKey struct{}

var ErrSendAlreadyKept = errors.New("this send is already kept here")

// WithQueuedSend is a local caller's choice to return after durable storage.
// id correlates that caller's optimistic turn; it grants no received authority.
// Existing CLI sends retain their immediate delivery/receipt behavior.
func WithQueuedSend(ctx context.Context, id string) context.Context {
	if id == "" {
		id = protocol.NewID()
	}
	return context.WithValue(ctx, queuedSendKey{}, id)
}
func queuedSend(ctx context.Context) bool { return ctx.Value(queuedSendKey{}) != nil }
func sendID(ctx context.Context) (string, error) {
	if id, ok := ctx.Value(proposalSendIDKey{}).(string); ok {
		return id, nil
	}
	if id, ok := ctx.Value(queuedSendKey{}).(string); ok {
		if !protocol.ValidID(id) {
			return "", errors.New("invalid local send id")
		}
		return id, nil
	}
	return protocol.NewID(), nil
}

type backgroundPosts struct {
	sync.Mutex
	done          chan struct{}
	cancel        context.CancelFunc
	archiveParent context.Context // latest stream wake, for canceled-stream handoff
	again, closed bool
}

// A send that failed transiently (no answer, 5xx, 429) is tried again soon,
// not only at the next Hub ping: 1 s after the failure, then 2 s, 4 s … up to
// sendRetryMax while sends keep failing (owner decision, October 31). A
// handover resets the wait. Nothing is scheduled while sends succeed or a
// copy only waits on its own gate, so an idle daemon still makes no
// requests; one pending wake covers every queued copy.
const sendRetryMax = 45 * time.Second // the stream's reconnect cap

type sendRetry struct {
	sync.Mutex
	timer  *time.Timer
	wait   time.Duration
	closed bool
}

func (r *sendRetry) succeeded() {
	r.Lock()
	r.wait = 0
	r.Unlock()
}

func (a *Agent) retrySendSoon(err error) {
	var he *HubError
	if !transient(err) || refusedUntilUpdate(err) || errors.As(err, &he) && he.Code == codeUpdateRequired || errors.Is(err, context.Canceled) {
		return // fails the same way again, waits for the updated program, or this daemon is stopping
	}
	r := &a.sendRetry
	r.Lock()
	defer r.Unlock()
	if r.closed || r.timer != nil {
		return
	}
	wait := max(r.wait, time.Second)
	r.wait = min(2*wait, sendRetryMax)
	r.timer = time.AfterFunc(wait, func() {
		r.Lock()
		r.timer = nil
		closed := r.closed
		r.Unlock()
		if !closed {
			a.kickNow() // a running daemon's own sync; nothing without one
		}
	})
}

// Only a wake is in memory. The encrypted outbox is the queue, including on
// crashes. Every pass uses the same retry path and durable insertion order.
func (a *Agent) postOutboxBackground() {
	a.initFlushLanes()
	select {
	case a.flushWake <- struct{}{}:
	default:
	}
	p := &a.posting
	p.Lock()
	defer p.Unlock()
	if p.closed {
		return
	}
	if p.done != nil {
		p.again = true
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel, p.done = cancel, make(chan struct{})
	done := p.done
	go func() {
		defer cancel()
		for {
			if err := a.flushReceipts(ctx); err != nil && ctx.Err() == nil {
				a.Logf("receipts: %v", err)
			}
			if err := a.flushOutbox(ctx, false); err != nil && ctx.Err() == nil {
				a.Logf("outbox: %v", err)
			}
			if err := a.flushReceipts(ctx); err != nil && ctx.Err() == nil {
				a.Logf("receipts: %v", err)
			}
			a.NoteChange()
			p.Lock()
			if p.again && !p.closed && ctx.Err() == nil {
				p.again = false
				p.Unlock()
				continue
			}
			p.done = nil
			close(done)
			p.Unlock()
			return
		}
	}()
}

func (a *Agent) stopBackgroundPosts() {
	a.sendRetry.Lock()
	a.sendRetry.closed = true
	if a.sendRetry.timer != nil {
		a.sendRetry.timer.Stop()
		a.sendRetry.timer = nil
	}
	a.sendRetry.Unlock()
	p := &a.posting
	p.Lock()
	p.closed = true
	done, cancel := p.done, p.cancel
	p.Unlock()
	if done == nil {
		return
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cancel() // HTTP is context-bound; stop before closing the DB/spool.
		<-done
	}
}

func (a *Agent) queuedConv(copies []outCopy, localID, lid string) ConvSent {
	sent := ConvSent{ID: localID, LID: lid, State: protocol.StateDelivered}
	if len(copies) > 0 {
		sent.ID = copies[0].env.ID
	}
	for _, c := range copies {
		cp := ConvCopy{ID: c.env.ID, To: c.env.To, State: c.state, Detail: c.why}
		if rank(cp.State) < rank(sent.State) {
			sent.State, sent.Detail = cp.State, cp.Detail
		}
		sent.Copies = append(sent.Copies, cp)
	}
	a.NoteChange()
	a.postOutboxBackground()
	return sent
}

// The caller's correlation is single-use in this store. Reusing it cannot
// mint a second set of physical envelopes, even with concurrent requests.
func (a *Agent) queuedClaim(ctx context.Context, conv string, claim func(*sql.Tx, string) error) func(*sql.Tx, string) error {
	if !queuedSend(ctx) {
		return claim
	}
	id, _ := sendID(ctx)
	return func(tx *sql.Tx, first string) error {
		var n int
		err := tx.QueryRow(`SELECT count(*) FROM (SELECT id FROM outbox WHERE id=? OR lid=? UNION ALL SELECT id FROM inbox WHERE (id=? OR lid=?) AND sender=? AND local=1)`, id, id, id, id, a.Address).Scan(&n)
		if err != nil {
			return err
		}
		if n > 0 {
			return ErrSendAlreadyKept
		}
		if dispatchID, _ := ctx.Value(scheduledDispatchKey{}).(string); dispatchID != id {
			var reserved bool
			if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM scheduled_sends WHERE id=?)`, id).Scan(&reserved); err != nil {
				return err
			}
			if reserved {
				return ErrScheduledIDConflict
			}
		}
		if claim != nil {
			return claim(tx, first)
		}
		return nil
	}
}
