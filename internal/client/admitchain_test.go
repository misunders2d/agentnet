package client

import (
	"context"
	"encoding/json"
	"io"
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

// A group reaction, edit or deletion asked each member's chain one after another
// on the same goroutine.
func TestHeldChainRequestHoldsAGroupReaction(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	conv := p.State.Conv
	dm := newDM(t, w.alice, w.bob)
	turn, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindMessage, Origin: envelope.OriginUI, Body: "reacted to"})
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(20 * time.Second); !w.bob.shows(t, conv, "reacted to"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the turn never came in")
		}
	}
	ref, err := w.alice.RefOf(conv, turn.ID, "out")
	if err != nil {
		t.Fatal(err)
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
	started := time.Now()
	if _, err = w.alice.React(tctx(t), ref, "👍", false); err != nil {
		t.Fatal(err)
	}
	sendTurn(t, w.alice, dm, "a direct message behind it")
	limit := 2*admitChainBudget + 3*time.Second
	for !w.bob.shows(t, dm, "a direct message behind it") {
		if time.Since(started) > limit {
			t.Fatalf("the stream stayed stopped for %v behind a group reaction", time.Since(started))
		}
		time.Sleep(10 * time.Millisecond)
	}
	hold.Store(false)
	for deadline := time.Now().Add(40 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		m, _ := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.Body == "reacted to" }) // each member has its own copy id
		if slices.ContainsFunc(m.Reactions, func(v ReactionView) bool { return v.Emoji == "👍" }) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the held reaction was never looked at again")
		}
	}
}

// A new chat's root naming a step of its creator's chain not pinned here asked
// for it with the whole request budget, on the same goroutine.
func TestHeldChainRequestHoldsANewChatNamingANewerStep(t *testing.T) {
	w, carol, _, _ := groupTurnsFixture(t)
	behind := newDM(t, carol, w.bob)
	sendTurn(t, carol, behind, "before")
	for deadline := time.Now().Add(20 * time.Second); !w.bob.shows(t, behind, "before"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("carol's message never came in")
		}
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
	if _, err := w.alice.RenamePerson(tctx(t), "Alice again"); err != nil {
		t.Fatal(err)
	}
	dm, err := w.alice.CreateDM(tctx(t), w.bob.Address)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	sendTurn(t, w.alice, dm, "in a new chat")
	for w.bob.heldForTest(t, reasonProof) == 0 {
		if time.Since(started) > 2*admitChainBudget+3*time.Second {
			t.Fatalf("not held as waiting after %v", time.Since(started))
		}
		time.Sleep(10 * time.Millisecond)
	}
	sendTurn(t, carol, behind, "behind it")
	for !w.bob.shows(t, behind, "behind it") {
		if time.Since(started) > 2*admitChainBudget+3*time.Second {
			t.Fatalf("the stream stayed stopped for %v behind a new chat", time.Since(started))
		}
		time.Sleep(10 * time.Millisecond)
	}
	hold.Store(false)
	for deadline := time.Now().Add(40 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if msgs, err := w.bob.ConversationMessages(dm); err == nil && slices.ContainsFunc(msgs, func(m ConvMessage) bool { return m.Body == "in a new chat" }) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the held new chat was never looked at again")
		}
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

// MEL-748: what the reader asked the relay outside refreshAdmitting waited out
// the 30 s request timeout per person, per attempt, with every later message
// and ping behind it. A group's new step reaches several such asks.
func TestHeldRelayAskStopsNoMessageBehindAGroupStep(t *testing.T) {
	w, _, p, _ := groupTurnsFixture(t)
	dm := newDM(t, w.alice, w.bob)
	var hold atomic.Bool
	var asked atomic.Int32
	hold.Store(true)
	defer hold.Store(false)
	w.hub.SetHold(func(r *http.Request) {
		if bobAsksChain(w, r) {
			asked.Add(1)
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	defer w.hub.SetHold(nil)
	var renamed GroupContext
	for _, title := range []string{"A held step", "Another held step", "A third held step"} {
		var err error
		if renamed, err = w.alice.RenameGroup(tctx(t), p.State.Conv, title); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "a request held", func() bool { return asked.Load() > 0 })
	started := time.Now()
	sendTurn(t, w.alice, dm, "a direct message behind the step")
	limit := 2*admitChainBudget + 3*time.Second
	for !w.bob.shows(t, dm, "a direct message behind the step") {
		if time.Since(started) > limit {
			t.Fatalf("the stream stayed stopped for %v behind a held request", time.Since(started))
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got, e := w.bob.GroupContext(p.State.Conv); e == nil && got.State.Hash() == renamed.State.Hash() {
		t.Fatal("the group's step was accepted without the rosters it depends on")
	}
	hold.Store(false)
	eventually(t, "the held step looked at again", func() bool {
		got, e := w.bob.GroupContext(p.State.Conv)
		return e == nil && got.State.Hash() == renamed.State.Hash()
	})
}

// A held acknowledgement of the Hub's ping held every message behind it.
func TestHeldPingAckStopsNoMessage(t *testing.T) {
	w := newWorld(t, "")
	w.bob.kick = func() {}
	var hold atomic.Bool
	hold.Store(true)
	defer hold.Store(false)
	w.hub.SetHold(func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/stream/ack") {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	defer w.hub.SetHold(nil)
	done := make(chan error, 1)
	ping, err := json.Marshal(protocol.PingAck{Conn: "held-connection"})
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- w.bob.dispatch(tctx(t), "ping", string(ping)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the stream's reader waited for the acknowledgement")
	}
}

// What the reader asks the relay ends with the message's budget, for every
// kind of request, and the failure can be told from any other.
func TestReaderBudgetEndsEveryRelayRequest(t *testing.T) {
	w := newWorld(t, "")
	var hold atomic.Bool
	hold.Store(true)
	defer hold.Store(false)
	w.hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == w.bob.Address && strings.HasPrefix(r.URL.Path, "/v1/agents/") {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	defer w.hub.SetHold(nil)
	for name, ask := range map[string]func(context.Context) error{
		"answer": func(ctx context.Context) error { return w.bob.hub.do(ctx, "GET", "/v1/agents/alice/laptop", nil, nil) },
		"range": func(ctx context.Context) error {
			return w.bob.hub.getRange(ctx, "/v1/agents/alice/laptop", 0, 1, io.Discard)
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := withReaderBudget(tctx(t), time.Now().Add(200*time.Millisecond))
			started := time.Now()
			err := ask(ctx)
			if !transient(err) || !readerBudgetSpent(ctx) {
				t.Fatalf("after %v: %v", time.Since(started), err)
			}
			if took := time.Since(started); took > 2*time.Second {
				t.Fatalf("the request outlived its budget by %v", took)
			}
			if err := ask(withReaderBudget(tctx(t), time.Now())); !transient(err) {
				t.Fatalf("a spent budget still asked: %v", err)
			}
		})
	}
	// Away from the reader, an answer that matters waits as long as it may.
	if until, ok := readerBudgetEnd(withoutReaderBudget(withReaderBudget(tctx(t), time.Now()))); ok {
		t.Fatalf("still bounded by %v", until)
	}
}

// An unknown sender cannot wait held, since the look at held messages needs its
// key. The stream ends as before, within the budget, and nothing is decided.
func TestLateDirectoryAnswerIsAskedAgainWithinTheBudget(t *testing.T) {
	w := newWorld(t, "")
	carol := proofReader(t, w, "carol")
	persons(t, w.alice, w.bob)
	if _, err := carol.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "from someone new"}); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := carol.store.db.QueryRow(`SELECT envelope FROM outbox WHERE recipient=?`, w.bob.Address).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.store.db.Exec(`DELETE FROM peers WHERE address=?`, carol.Address); err != nil {
		t.Fatal(err)
	}
	seen, err := w.bob.store.seen(env.ID)
	if err != nil || seen {
		t.Fatalf("already here: %v %v", seen, err)
	}
	var hold atomic.Bool
	hold.Store(true)
	defer hold.Store(false)
	w.hub.SetHold(func(r *http.Request) {
		if r.Header.Get(protocol.HeaderAgent) == w.bob.Address && strings.HasPrefix(r.URL.Path, "/v1/agents/") {
			for hold.Load() {
				time.Sleep(10 * time.Millisecond)
			}
		}
	})
	defer w.hub.SetHold(nil)
	started := time.Now()
	err = w.bob.storeReceived(tctx(t), env)
	if !transient(err) {
		t.Fatalf("not left for the Hub to push again: %v", err)
	}
	if took := time.Since(started); took > admitChainBudget+2*time.Second {
		t.Fatalf("the reader waited %v", took)
	}
	if seen, e := w.bob.store.seen(env.ID); e != nil || seen {
		t.Fatalf("decided without its sender's key: %v %v", seen, e)
	}
	hold.Store(false)
	if err := w.bob.storeReceived(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if seen, e := w.bob.store.seen(env.ID); e != nil || !seen {
		t.Fatalf("never came in: %v %v", seen, e)
	}
}
