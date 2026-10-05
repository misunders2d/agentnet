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
	t.Parallel()
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

// Pending auxiliary copies keep their own delivery gate closed without
// starving a readable turn to the same recipient in the same conversation.
func TestQueuedSendAuxiliaryCopiesDoNotStarveTurns(t *testing.T) {
	t.Parallel()
	for _, sub := range []string{envelope.SubHistory, envelope.SubClear} {
		t.Run(sub, func(t *testing.T) {
			w, conv := queuedDMWorld(t)
			id := protocol.NewID()
			raw, _ := json.Marshal(envelope.Envelope{V: 3, ID: id, From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage})
			ref, cap := "", ""
			if sub == envelope.SubClear {
				ref, cap = protocol.NewID(), protocol.CapConvClear
			}
			// A legacy waiting history copy has no control ref. A clear
			// copy has its normal ref/capability; neither has a send order.
			if _, err := w.alice.store.db.Exec(`INSERT INTO outbox(id,recipient,body,envelope,state,created_at,conv,lid,kind,sub,ref_id,required_cap)
				VALUES(?,?,'',?,?,1,?,?,'message',?,nullif(?,''),nullif(?,''))`, id, w.bob.Address, string(raw), stateConvWaiting, conv, protocol.NewID(), sub, ref, cap); err != nil {
				t.Fatal(err)
			}
			sent := queuedTurn(t, w.alice, conv, "readable after "+sub)
			eventually(t, "turn after pending "+sub, func() bool { return inboxCount(t, w.bob, "id=?", sent.ID) == 1 })
			if got := outboxState(t, w.alice, id); got != stateConvWaiting {
				t.Fatalf("auxiliary gate changed: %s", got)
			}
		})
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
	t.Parallel()
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

// Topic metadata and the caller's logical correlation are durable while the
// first network post is still blocked; the next turn keeps that same topic.
func TestQueuedSendTopicSurvivesBlockedPost(t *testing.T) {
	w, conv := queuedDMWorld(t)
	p := pausePosts(w.alice)
	t.Cleanup(func() {
		select {
		case <-p.release:
		default:
			close(p.release)
		}
	})
	topic := protocol.NewID()
	for _, body := range []string{"topic first", "topic second"} {
		id := protocol.NewID()
		ctx, cancel := context.WithTimeout(WithQueuedSend(tctx(t), id), 3*time.Second)
		sent, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: body, Topic: topic})
		cancel()
		if err != nil || sent.LID != id || sent.State != stateQueued {
			t.Fatalf("queued topic send: %+v %v", sent, err)
		}
		var kept string
		if err := w.alice.store.db.QueryRow(`SELECT topic FROM outbox WHERE id=? AND lid=?`, sent.ID, id).Scan(&kept); err != nil || kept != topic {
			t.Fatalf("durable topic %q: %v", kept, err)
		}
	}
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("background post never started")
	}
	if n := inboxCount(t, w.bob, "body LIKE 'topic %'"); n != 0 {
		t.Fatalf("post was not blocked: %d turns", n)
	}
	close(p.release)
	eventually(t, "two signed topic turns delivered", func() bool {
		return inboxCount(t, w.bob, "body LIKE 'topic %' AND topic=?", topic) == 2
	})
	ts, err := w.bob.ChatTopics(conv)
	if err != nil || len(ts) != 1 || ts[0].ID != topic {
		t.Fatalf("one retained topic: %+v %v", ts, err)
	}
}

// Member and guest requests must keep the UI correlation as their shared LID
// and executable host ID. That correlation never grants the guest permission.
func TestQueuedSendHumanRequestTopicCorrelation(t *testing.T) {
	for _, guest := range []bool{false, true} {
		name := "member"
		if guest {
			name = "guest"
		}
		t.Run(name, func(t *testing.T) {
			w, carol, _, _, stub, ap, _ := guestAssistant(t)
			sender := w.alice
			if guest {
				sender = carol
			}
			id, topic := protocol.NewID(), protocol.NewID()
			ctx := WithQueuedSend(tctx(t), id)
			sent, err := sender.AskAgentInTopic(ctx, ap.PID, envelope.KindQuestion, "queued topic request", topic, nil)
			if err != nil || sent.LID != id {
				t.Fatalf("queued request correlation: %+v %v", sent, err)
			}
			eventually(t, "exact host request copy", func() bool {
				return inboxCount(t, w.bob, "id=? AND lid=? AND topic=? AND human IS NOT NULL", id, id, topic) == 1
			})
			if _, err := sender.AskAgentInTopic(ctx, ap.PID, envelope.KindQuestion, "reused correlation", topic, nil); err == nil {
				t.Fatal("queued request minted a second batch with the same correlation")
			}
			if guest {
				waitState(t, w.bob, id, stateAwaiting)
				if stub.runs() != 0 {
					t.Fatal("unapproved queued guest request ran")
				}
			}
		})
	}
}
