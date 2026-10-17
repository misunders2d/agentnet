package client

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// The signed local owner decision must exist before any unrelated recipient's
// slow transport can stall the invitation. No manual acceptance is involved.
func TestSelfConsentBeforeFanout(t *testing.T) {
	for _, scenario := range []string{"dm", "group", "pending group retry"} {
		t.Run(scenario, func(t *testing.T) { testSelfConsentBeforeFanout(t, scenario) })
	}
}

func testSelfConsentBeforeFanout(t *testing.T, scenario string) {
	type inviteCallKey struct{}
	entered, release := make(chan string, 1), make(chan struct{})
	var once, released sync.Once
	previousWrap := wrapTransport
	wrapTransport = func(base http.RoundTripper) http.RoundTripper {
		if previousWrap != nil {
			base = previousWrap(base)
		}
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			// Other incoming fixture traffic may still refresh proof after Run
			// stops. Only this InviteAgent call's fanout is the test boundary.
			if r.Context().Value(inviteCallKey{}) != true {
				return base.RoundTrip(r)
			}
			once.Do(func() { entered <- r.Method + " " + r.URL.RequestURI() })
			select {
			case <-release:
			case <-r.Context().Done():
			}
			return nil, errors.New("isolated fanout unavailable")
		})
	}
	t.Cleanup(func() { wrapTransport = previousWrap })
	defer func() { released.Do(func() { close(release) }) }()
	var w *world
	var conv, pid string
	var stop func()
	if scenario == "dm" {
		w, conv, stop = selfConsentWorld(t)
	} else {
		var packet GroupContext
		var stops map[*Agent]func()
		w, _, packet, stops = groupTurnsFixture(t)
		conv, stop = packet.State.Conv, stops[w.alice]
	}
	stop()
	if scenario == "pending group retry" {
		p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "slow fanout")
		if err != nil || p.State != PartInvited {
			t.Fatalf("pending invite: %+v %v", p, err)
		}
		pid = p.PID
	}
	if err := w.alice.SetResponder(&Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	// Force unrelated traffic before the invitation: the old any-request
	// barrier incorrectly entered here, before its invitation even existed.
	probeCtx, probeCancel := context.WithCancel(tctx(t))
	probeDone := make(chan error, 1)
	go func() { probeDone <- w.alice.hub.do(probeCtx, "GET", "/v1/version", nil, nil) }()
	select {
	case request := <-entered:
		probeCancel()
		<-probeDone
		t.Fatalf("fanout barrier intercepted unrelated request: %s", request)
	case err := <-probeDone:
		probeCancel()
		if err != nil {
			t.Fatalf("unrelated request: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.WithValue(tctx(t), inviteCallKey{}, true))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = w.alice.InviteAgent(ctx, conv, w.alice.Address, nil, nil, "slow fanout")
	}()
	finish := func() {
		released.Do(func() { close(release) })
		<-done
		cancel()
	}
	defer finish()
	select {
	case request := <-entered:
		t.Logf("invitation fanout blocked at %s", request)
	case <-time.After(5 * time.Second):
		t.Fatal("invitation never reached the transport")
	}
	if pid == "" {
		if err := w.alice.store.db.QueryRow(`SELECT pid FROM participation_events WHERE conv=? AND type=? AND json_extract(event,'$.note')=?`, conv, protocol.EventInvite, "slow fanout").Scan(&pid); err != nil {
			t.Fatal(err)
		}
	}
	fp := w.alice.Self().Fingerprint()
	if n := decisionsBy(t, w.alice, conv, pid, fp); n != 1 {
		t.Fatalf("local owner decision blocked behind fanout: %d decisions", n)
	}
	p := stateAt(t, w.alice, pid)
	if p.State != PartActive || p.Decision == "" || !p.OwnerInvited {
		t.Fatalf("local signed acceptance missing: %+v", p)
	}
	t.Logf("signed local acceptance at %s while fanout blocked", time.Now().UTC().Format(time.RFC3339Nano))
	// A transport failure neither rolls back the decision nor loses its copies.
	finish()
	home := w.alice.home
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reopened.Close() })
	if got := stateAt(t, reopened, pid); got.State != PartActive || got.Decision != p.Decision {
		t.Fatalf("acceptance lost on reopen: %+v", got)
	}
	runAgent(t, reopened)
	defer func() {
		if !t.Failed() {
			return
		}
		t.Logf("host: %+v; peer: %+v", stateAt(t, reopened, pid), stateAt(t, w.bob, pid))
		rows, _ := reopened.store.db.Query(`SELECT sub,state,coalesce(error,''),body FROM outbox WHERE pid=?`, pid)
		if rows != nil {
			defer rows.Close()
			for rows.Next() {
				var sub, state, problem, body string
				rows.Scan(&sub, &state, &problem, &body)
				t.Logf("outbox %s %s %s %s", sub, state, problem, body)
			}
		}
	}()
	eventually(t, "peer receives signed acceptance after restart", func() bool {
		got := stateAt(t, w.bob, pid)
		return got.State == PartActive && got.Decision == p.Decision
	})
	if n := decisionsBy(t, reopened, conv, pid, fp); n != 1 {
		t.Fatalf("restart signed %d decisions", n)
	}
	t.Logf("peer active at %s without manual acceptance", time.Now().UTC().Format(time.RFC3339Nano))
}
