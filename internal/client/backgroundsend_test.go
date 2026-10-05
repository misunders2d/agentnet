package client

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type pausedPosts struct {
	base    http.RoundTripper
	entered chan string
	release chan struct{}
	mu      sync.Mutex
	ids     []string
}

func (p *pausedPosts) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "POST" && r.URL.Path == "/v1/messages" {
		body, _ := r.GetBody()
		var env envelope.Envelope
		json.NewDecoder(body).Decode(&env)
		body.Close()
		p.mu.Lock()
		p.ids = append(p.ids, env.ID)
		p.mu.Unlock()
		select {
		case p.entered <- env.ID:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		select {
		case <-p.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return p.base.RoundTrip(r)
}
func queuedDMWorld(t *testing.T) (*world, string) {
	t.Helper()
	w := newWorld(t, "")
	persons(t, w.alice, w.bob)
	runAgent(t, w.bob)
	conv := newDM(t, w.alice, w.bob)
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	return w, conv
}
func pausePosts(a *Agent) *pausedPosts {
	p := &pausedPosts{base: a.hub.http.Transport, entered: make(chan string, 8), release: make(chan struct{})}
	a.hub.http.Transport = p
	return p
}
func queuedTurn(t *testing.T, a *Agent, conv, body string) ConvSent {
	t.Helper()
	id := protocol.NewID()
	ctx, cancel := context.WithTimeout(WithQueuedSend(context.Background(), id), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	var sent ConvSent
	var err error
	go func() { sent, err = a.SendConv(ctx, conv, ConvOutgoing{Body: body}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("send waited for Hub posting")
	}
	if err != nil {
		t.Fatal(err)
	}
	if sent.LID != id || sent.State != stateQueued {
		t.Fatalf("queued correlation: %+v want %s", sent, id)
	}
	return sent
}
func TestQueuedSendReturnsBeforePostAndOrdersTwoTurns(t *testing.T) {
	w, conv := queuedDMWorld(t)
	p := pausePosts(w.alice)
	t.Cleanup(func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
	})
	a := queuedTurn(t, w.alice, conv, "first")
	select {
	case id := <-p.entered:
		if id != a.ID {
			t.Fatalf("post %s want %s", id, a.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("background post never started")
	}
	b := queuedTurn(t, w.alice, conv, "second")
	select {
	case id := <-p.entered:
		t.Fatalf("second post %s overtook blocked first", id)
	case <-time.After(80 * time.Millisecond):
	}
	close(p.release)
	eventually(t, "two delivered ordered turns", func() bool { return inboxCount(t, w.bob, "body IN ('first','second')") == 2 })
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.ids) != 2 || p.ids[0] != a.ID || p.ids[1] != b.ID {
		t.Fatalf("post order %v", p.ids)
	}
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
}
func TestQueuedSendLostReceiptRetryIsOneDelivery(t *testing.T) {
	w, conv := queuedDMWorld(t)
	f := injectFaults(w.alice)
	f.add("POST", "/v1/messages", 1, true)
	sent := queuedTurn(t, w.alice, conv, "lost response")
	eventually(t, "first pass completed", func() bool {
		w.alice.posting.Lock()
		defer w.alice.posting.Unlock()
		return w.alice.posting.done == nil
	})
	if state := outboxState(t, w.alice, sent.ID); state != stateQueued {
		t.Fatal(state)
	}
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "one retained delivery", func() bool { return inboxCount(t, w.bob, "body='lost response'") == 1 })
	if state := outboxState(t, w.alice, sent.ID); state != protocol.StateCustody && state != protocol.StateDelivered {
		t.Fatal(state)
	}
	if err := w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, w.bob, "body='lost response'"); n != 1 {
		t.Fatal(n)
	}
}
func TestQueuedSendCloseWaitsForInFlight(t *testing.T) {
	w, conv := queuedDMWorld(t)
	p := pausePosts(w.alice)
	t.Cleanup(func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
	})
	queuedTurn(t, w.alice, conv, "close waits")
	<-p.entered
	closed := make(chan error, 1)
	go func() { closed <- w.alice.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("closed during post: %v", err)
	case <-time.After(80 * time.Millisecond):
	}
	close(p.release)
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after post")
	}
}
func TestQueuedSendRestartResumesSameEnvelopeOnce(t *testing.T) {
	w, conv := queuedDMWorld(t)
	p := pausePosts(w.alice)
	sent := queuedTurn(t, w.alice, conv, "restart once")
	<-p.entered
	// A canceled in-flight request simulates interruption before custody.
	w.alice.posting.Lock()
	w.alice.posting.cancel()
	w.alice.posting.Unlock()
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if state := outboxState(t, resumed, sent.ID); state != stateQueued {
		t.Fatal(state)
	}
	if err := resumed.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "one delivery after restart", func() bool { return inboxCount(t, w.bob, "body='restart once'") == 1 })
	if err := resumed.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if n := inboxCount(t, w.bob, "body='restart once'"); n != 1 {
		t.Fatal(n)
	}
}
func TestQueuedSendRejectsInvalidCorrelationBeforeSaving(t *testing.T) {
	w, conv := queuedDMWorld(t)
	if _, err := w.alice.SendConv(WithQueuedSend(tctx(t), "bad id"), conv, ConvOutgoing{Body: "invalid"}); err == nil {
		t.Fatal("accepted invalid local id")
	}
	var n int
	w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE body='invalid'`).Scan(&n)
	if n != 0 {
		t.Fatal(n)
	}
}

func TestQueuedSendRejectsReusedCorrelationBeforeSaving(t *testing.T) {
	w, conv := queuedDMWorld(t)
	p := pausePosts(w.alice)
	t.Cleanup(func() { close(p.release) })
	id := protocol.NewID()
	ctx := WithQueuedSend(tctx(t), id)
	if _, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "one correlation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "one correlation"}); err == nil {
		t.Fatal("minted another physical envelope for the same local turn")
	}
	var n int
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE body='one correlation'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("saved logical turns: %d, %v", n, err)
	}
}

func TestQueuedSendCloseCancelsBlockedPostAfterBound(t *testing.T) {
	w, conv := queuedDMWorld(t)
	p := pausePosts(w.alice)
	sent := queuedTurn(t, w.alice, conv, "bounded close")
	<-p.entered
	start := time.Now()
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 4*time.Second || elapsed > 7*time.Second {
		t.Fatalf("Close bound: %s", elapsed)
	}
	resumed, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if state := outboxState(t, resumed, sent.ID); state != stateQueued {
		t.Fatalf("interrupted send not kept queued: %s", state)
	}
}
