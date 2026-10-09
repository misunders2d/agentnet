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
	again, closed bool
}

// Only a wake is in memory. The encrypted outbox is the queue, including on
// crashes. Every pass uses the same retry path and durable insertion order.
func (a *Agent) postOutboxBackground() {
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
			if err := a.FlushOutbox(ctx); err != nil && ctx.Err() == nil {
				a.Logf("outbox: %v", err)
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
		err := tx.QueryRow(`SELECT count(*) FROM (SELECT id FROM outbox WHERE conv=? AND lid=? UNION ALL SELECT id FROM inbox WHERE conv=? AND lid=? AND sender=? AND local=1)`, conv, id, conv, id, a.Address).Scan(&n)
		if err != nil {
			return err
		}
		if n > 0 {
			return errors.New("this send is already kept here")
		}
		if claim != nil {
			return claim(tx, first)
		}
		return nil
	}
}
