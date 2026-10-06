package client

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type pausedWorkerDelivery struct {
	base             http.RoundTripper
	entered, release chan struct{}
	once             sync.Once
}

func (p *pausedWorkerDelivery) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "POST" && r.URL.Path == "/v1/messages" {
		p.once.Do(func() { close(p.entered) })
		select {
		case <-p.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return p.base.RoundTrip(r)
}

// The messenger is a separate Agent handle, as in the desktop bridge. Its
// durable local job must wake the daemon without receiving a Hub message or
// heartbeat, and while the remote copy remains blocked in background delivery.
func TestWorkerQueuedOwnRequestWakesBeforeRemoteDelivery(t *testing.T) {
	st := installAgentStub(t)
	w, conv, lids, stopBob := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, lids[:2], nil)
	stopBob() // no push stream or heartbeat can hide a missing local wake
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWorker, err := w.bob.startWorker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stopWorker()
	st.mode("sleep")
	sender, err := Open(w.bob.home)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	paused := &pausedWorkerDelivery{base: sender.hub.http.Transport, entered: make(chan struct{}), release: make(chan struct{})}
	sender.hub.http.Transport = paused
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(paused.release) }) }
	defer release()
	// Let the startup scan become idle. There is deliberately no periodic wake.
	time.Sleep(100 * time.Millisecond)
	sent, err := sender.AskAgent(WithQueuedSend(tctx(t), ""), pid, envelope.KindQuestion, "answer now without waiting for remote delivery")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-paused.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("remote background copy did not enter the delivery gate")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, e := os.Stat(st.log + ".started"); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("committed own request did not start while remote copy was blocked and no Hub heartbeat was available")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := jobState(t, w.bob, sent.ID); got != stateRunning {
		t.Fatalf("local job state %q", got)
	}
	release()
}

// Failed storage grants nothing to a wake callback. On success, the callback
// can already see the durable exact request on a separate connection.
func TestWorkerLocalJobWakeOnlyAfterCommit(t *testing.T) {
	w := newWorld(t, "")
	reader, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	request := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), LID: protocol.NewID(), From: w.alice.Address, To: w.alice.Address, Kind: envelope.KindQuestion, Body: "committed request", TS: time.Now().Unix()}
	wakes := 0
	w.alice.store.onJobReady = func() {
		wakes++
		var n int
		if e := reader.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND state=? AND local=1`, request.ID, stateAgentWaiting).Scan(&n); e != nil || n != 1 {
			t.Errorf("wake preceded committed request: %d %v", n, e)
		}
	}
	refused := errors.New("transaction refused")
	if err = w.alice.store.addConvOutbox(nil, request, func(*sql.Tx, string) error { return refused }, w.alice.Self().Fingerprint()); !errors.Is(err, refused) {
		t.Fatal(err)
	}
	if wakes != 0 {
		t.Fatal("rolled-back request woke worker")
	}
	if err = w.alice.store.addConvOutbox(nil, request, nil, w.alice.Self().Fingerprint()); err != nil {
		t.Fatal(err)
	}
	if wakes != 1 {
		t.Fatalf("committed local request wake count: %d", wakes)
	}
}
