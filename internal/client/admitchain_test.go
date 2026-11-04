package client

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func bobAsksChain(w *world, r *http.Request) bool {
	return r.Header.Get(protocol.HeaderAgent) == w.bob.Address && strings.HasPrefix(r.URL.Path, "/v1/persons/")
}

func (a *Agent) shows(t *testing.T, conv, body string) bool {
	t.Helper()
	msgs, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(msgs, func(m ConvMessage) bool { return m.Body == body })
}

func (a *Agent) heldForTest(t *testing.T, reason string) int {
	t.Helper()
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE reason=?`, reason).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sendTurn(t *testing.T, from *Agent, conv, body string) {
	t.Helper()
	if _, err := from.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: body}); err != nil {
		t.Fatal(err)
	}
}

// MEL-748: a received group message waited on the one goroutine reading the
// push stream for each member's chain, one after another. A request the relay
// held stopped every later message and ping until its 30 s ran out.
func TestHeldChainRequestStopsNoLaterMessage(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	dm := newDM(t, w.alice, w.bob)
	var hold atomic.Bool
	hold.Store(true)
	defer hold.Store(false)
	w.hub.SetHold(func(r *http.Request) {
		if bobAsksChain(w, r) {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	defer w.hub.SetHold(nil)
	started := time.Now()
	for _, body := range []string{"first turn", "second turn", "third turn"} {
		sendTurn(t, w.alice, p.State.Conv, body)
	}
	sendTurn(t, w.alice, dm, "a direct message behind them")
	limit := 2*admitChainBudget + 3*time.Second
	for !w.bob.shows(t, dm, "a direct message behind them") {
		if time.Since(started) > limit {
			t.Fatalf("the stream stayed stopped for %v behind a held chain request", time.Since(started))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Each group turn stays waiting, not shown: nothing was decided about them.
	for _, body := range []string{"first turn", "second turn", "third turn"} {
		if w.bob.shows(t, p.State.Conv, body) {
			t.Fatalf("%q was admitted without the rosters it depends on", body)
		}
	}
	hold.Store(false)
	for deadline := time.Now().Add(40 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if w.bob.shows(t, p.State.Conv, "first turn") && w.bob.shows(t, p.State.Conv, "second turn") && w.bob.shows(t, p.State.Conv, "third turn") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("held turns were never looked at again")
		}
	}
}

// A history file carrier and a participation history row checked each member's
// chain the same way, on the same goroutine.
func TestHeldChainRequestHoldsGroupHistoryCarriers(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	var hold atomic.Bool
	hold.Store(true)
	defer hold.Store(false)
	w.hub.SetHold(func(r *http.Request) {
		if bobAsksChain(w, r) {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	defer w.hub.SetHold(nil)
	index, size := 0, int64(1)
	body, err := json.Marshal(fileMsg{V: 1, Type: "request", LID: protocol.NewID(), Author: w.alice.Self().Fingerprint(), Hash: strings.Repeat("a", 64), SHA256: strings.Repeat("b", 64), Index: &index, Name: "file.bin", Size: &size})
	if err != nil {
		t.Fatal(err)
	}
	in := envelope.Inner{Conv: p.State.Conv, Replica: true, Body: string(body)}
	for name, admit := range map[string]func(func(string, string) error) error{
		"history file": func(h func(string, string) error) error {
			return w.bob.admitGroupFile(tctx(t), envelope.Envelope{ID: protocol.NewID()}, in, p.Root, w.alice.Self(), false, h)
		},
		"participation history": func(h func(string, string) error) error {
			return w.bob.admitGroupParticipationHistory(tctx(t), envelope.Envelope{ID: protocol.NewID()}, in, p.Root, w.alice.Self(), HistoryItem{}, false, h)
		},
	} {
		t.Run(name, func(t *testing.T) {
			reason := ""
			started := time.Now()
			err := admit(func(r, _ string) error { reason = r; return nil })
			if err != nil || reason != reasonProof {
				t.Fatalf("not held for its proof: reason %q err %v", reason, err)
			}
			if took := time.Since(started); took > admitChainBudget+3*time.Second {
				t.Fatalf("the stream's reader waited %v behind a held chain request", took)
			}
		})
	}
}

// A newer step the relay names and does not deliver: the message is not admitted
// on the older roster, the reader moves on within its budget, and the message
// comes in once the step is pinned.
func TestInboundWaitsHeldWhenANewerRosterIsSlow(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	conv := p.State.Conv
	me, _, err := w.alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	before, ok, err := w.bob.store.personByID(me.Person)
	if err != nil || !ok {
		t.Fatalf("alice is not pinned on bob: %v", err)
	}
	var hold atomic.Bool
	hold.Store(true)
	defer hold.Store(false)
	w.hub.SetHold(func(r *http.Request) {
		if bobAsksChain(w, r) {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	defer w.hub.SetHold(nil)
	if _, err = w.alice.RenamePerson(tctx(t), "Alice again"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	sendTurn(t, w.alice, conv, "after a rename")
	for w.bob.heldForTest(t, reasonProof) == 0 {
		if time.Since(started) > 2*admitChainBudget+3*time.Second {
			t.Fatalf("not held as waiting after %v", time.Since(started))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if w.bob.shows(t, conv, "after a rename") {
		t.Fatal("admitted before the newer roster was known")
	}
	hold.Store(false)
	for deadline := time.Now().Add(40 * time.Second); !w.bob.shows(t, conv, "after a rename"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("a held message was never looked at again")
		}
	}
	after, _, err := w.bob.store.personByID(me.Person)
	if err != nil || after.info.Seq <= before.info.Seq {
		t.Fatalf("the newer step was not pinned: seq %d after %d (%v)", after.info.Seq, before.info.Seq, err)
	}
}

// A chain request that fails once used to drop the stream, and the relay sent
// everything again. Now only that message waits, and comes in soon after.
func TestChainFailureDropsNoStream(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	streams := func() int {
		n := 0
		for _, r := range w.hub.Requests() {
			if r == w.bob.Address+" GET /v1/stream" {
				n++
			}
		}
		return n
	}
	before := streams()
	var fail atomic.Int32
	fail.Store(1)
	w.hub.SetHold(func(r *http.Request) {
		if bobAsksChain(w, r) && fail.Add(-1) >= 0 {
			panic(http.ErrAbortHandler)
		}
	})
	defer w.hub.SetHold(nil)
	started := time.Now()
	sendTurn(t, w.alice, p.State.Conv, "through a failed request")
	for !w.bob.shows(t, p.State.Conv, "through a failed request") {
		if time.Since(started) > 15*time.Second {
			t.Fatal("the message did not come in after one failed request")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if fail.Load() >= 0 {
		t.Fatal("no chain request failed: the test proved nothing")
	}
	if now := streams(); now != before {
		t.Fatalf("the stream was dropped: %d connections, was %d", now, before)
	}
}

// A relay that keeps failing is asked again after 1 s, doubling, not at once:
// every look at held messages must not itself start another look.
func TestFailingChainIsNotHammered(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	var failing atomic.Bool
	failing.Store(true)
	defer failing.Store(false)
	var asks atomic.Int64
	w.hub.SetHold(func(r *http.Request) {
		if bobAsksChain(w, r) && failing.Load() {
			asks.Add(1)
			panic(http.ErrAbortHandler)
		}
	})
	defer w.hub.SetHold(nil)
	sendTurn(t, w.alice, p.State.Conv, "while it fails")
	time.Sleep(6 * time.Second)
	// Three members, several paths; a look per kick would reach thousands.
	if n := asks.Load(); n == 0 || n > 60 {
		t.Fatalf("%d chain requests in 6 s", n)
	}
	if w.bob.shows(t, p.State.Conv, "while it fails") {
		t.Fatal("admitted without the rosters")
	}
	failing.Store(false)
	for deadline := time.Now().Add(60 * time.Second); !w.bob.shows(t, p.State.Conv, "while it fails"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("never looked at again after the relay recovered")
		}
	}
}
