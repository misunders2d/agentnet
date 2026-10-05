package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// AgentNet puts no time limit on agent work: a responder without the
// person's own limit runs as long as it takes (no 5-minute default), and a
// limit the person set is still honoured.
func TestResponderNoDefaultTimeLimit(t *testing.T) {
	r := Responder{Harness: "claude", Dir: t.TempDir()}
	if err := validateResponder(&r); err != nil || r.Timeout != 0 {
		t.Fatalf("default limit %v %v", r.Timeout, err)
	}
	r.Timeout = -time.Second
	if err := validateResponder(&r); err != nil || r.Timeout != 0 {
		t.Fatalf("negative limit %v %v", r.Timeout, err)
	}

	st := installStub(t, "slow")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, 0)
	if got, _ := w.bob.Responder(); got == nil || got.Timeout != 0 {
		t.Fatalf("stored %+v", got)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "take your time", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the answer without a limit", func() bool {
		m, ok := findReply(w.alice, q.ID)
		return ok && m.Status == envelope.StatusDone && strings.Contains(m.Body, "stub answer")
	})

	// The person's own limit still stops a run.
	setResponder(t, w.bob, "stub", st.dir, 200*time.Millisecond)
	q, err = w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "quick", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the person's limit", func() bool {
		m, ok := findReply(w.alice, q.ID)
		return ok && m.Status == envelope.StatusTimeout
	})
}

// A home where an earlier build stored its 5-minute default (on the
// default responder and on named agents) loses it once, at Open: no
// platform limit is left in force where no command or screen clears it.
// A limit the person set otherwise stays, and clearing happens only once.
func TestStoredOldDefaultLimitCleared(t *testing.T) {
	w := newWorld(t, "")
	a := w.bob
	dir := t.TempDir()
	setResponder(t, a, "claude", dir, oldDefaultLimit)
	five, err := a.CreateLocalAgent("Five", Responder{Harness: "claude", Dir: dir, Timeout: oldDefaultLimit})
	if err != nil {
		t.Fatal(err)
	}
	own, err := a.CreateLocalAgent("Own", Responder{Harness: "claude", Dir: dir, Timeout: 7 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`DELETE FROM config WHERE k = ?`, oldDefaultLimitGone); err != nil { // as a home of an earlier build
		t.Fatal(err)
	}
	limits := func(a *Agent) (def time.Duration, by map[string]time.Duration) {
		r, err := a.Responder()
		if err != nil || r == nil {
			t.Fatalf("responder %v %v", r, err)
		}
		agents, err := a.LocalAgents()
		if err != nil {
			t.Fatal(err)
		}
		by = map[string]time.Duration{}
		for _, e := range agents {
			by[e.Record.ID] = e.Responder.Timeout
		}
		return r.Timeout, by
	}
	opened, err := Open(a.home) // as the next start of the program
	if err != nil {
		t.Fatal(err)
	}
	def, by := limits(opened)
	opened.Close()
	if def != 0 || by[five.ID] != 0 || by[own.ID] != 7*time.Minute {
		t.Fatalf("after clearing: default %v, agents %v", def, by)
	}
	// Once: a five-minute limit the person sets afterwards stays.
	setResponder(t, a, "claude", dir, oldDefaultLimit)
	if err := a.store.clearOldDefaultLimit(); err != nil {
		t.Fatal(err)
	}
	if def, _ := limits(a); def != oldDefaultLimit {
		t.Fatalf("a limit set after the clearing was cleared: %v", def)
	}
}
