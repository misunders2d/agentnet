package client

import (
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

type delayedReceiptRT struct {
	base                   http.RoundTripper
	started, release, ping chan struct{}
	once, pingOnce         sync.Once
}

func (rt *delayedReceiptRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/messages/") && strings.HasSuffix(r.URL.Path, "/ack") {
		rt.once.Do(func() { close(rt.started) })
		select {
		case <-rt.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	if r.URL.Path == "/v1/stream/ack" {
		select {
		case <-rt.started:
			rt.pingOnce.Do(func() { close(rt.ping) })
		default:
		}
	}
	return rt.base.RoundTrip(r)
}

func TestReceiptStallDoesNotBlockStreamDelivery(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.StartConfig(t, hub.Config{DataDir: dir, Heartbeat: time.Second}, "127.0.0.1:0")
	alice := mustJoin(t, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "alice")
	code, err := alice.Invite(tctx(t), "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob := mustJoin(t, filepath.Join(t.TempDir(), "bob"), code, "laptop")
	w := &world{hub: h, alice: alice, bob: bob}
	rt := &delayedReceiptRT{base: w.bob.hub.http.Transport, started: make(chan struct{}), release: make(chan struct{}), ping: make(chan struct{})}
	w.bob.hub.http.Transport = rt
	var release sync.Once
	defer release.Do(func() { close(rt.release) })
	runAgent(t, w.bob)
	first, err := w.alice.Send(tctx(t), w.bob.Address, "first", "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-rt.started:
	case <-time.After(5 * time.Second):
		t.Fatal("receipt was not attempted")
	}
	second, err := w.alice.Send(tctx(t), w.bob.Address, "second", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "second message stored while first receipt is blocked", func() bool {
		seen, err := w.bob.store.seen(second.ID)
		return err == nil && seen
	})
	select {
	case <-rt.ping:
	case <-time.After(5 * time.Second):
		t.Fatal("receipt blocked ping acknowledgement")
	}
	if got := state(t, w.alice, first.ID); got != protocol.StateCustody {
		t.Fatalf("delivery claimed before receipt: %s", got)
	}
	release.Do(func() { close(rt.release) })
	eventually(t, "both receipts drain", func() bool {
		return state(t, w.alice, first.ID) == protocol.StateDelivered && state(t, w.alice, second.ID) == protocol.StateDelivered
	})
	if n := count(t, w.bob, "inbox"); n != 2 {
		t.Fatalf("redelivery duplicated messages: %d", n)
	}
}
