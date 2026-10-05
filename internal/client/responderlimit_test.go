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
