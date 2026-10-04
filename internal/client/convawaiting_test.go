package client

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// convExec is the requester's view of its request lid in conv: where the
// executing device last said it stands, or nil.
func convExec(t *testing.T, a *Agent, conv, lid string) *ExecView {
	t.Helper()
	m, n := convMsg(t, a, conv, func(m ConvMessage) bool { return m.LID == lid && m.Sub == "" })
	if n != 1 {
		return nil
	}
	return m.Exec
}

// A conversation request to another person's agent carries, on the
// requester's copy, where its executing device says it stands, as a device
// message does: waiting for that person's OK (told as soon as the host's
// look leaves it waiting, without anyone opening it there), then running,
// then settled by the result the host sent. Only the device a request is
// for speaks for it. Whatever a look at requests writes (waiting for the
// person, or not run because the participation ended) is reported for
// telling.
func TestConvRequestAwaitingToldToRequester(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, stopBob := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	task, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "restart the deploy")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)
	eventually(t, "alice told the task awaits bob", func() bool {
		e := convExec(t, w.alice, conv, task.LID)
		return e != nil && e.State == "awaiting" && e.Host == w.bob.Address && e.Detail == BlockerAcceptance
	})
	if st.runs() != 0 {
		t.Fatalf("an unaccepted task ran %d time(s)", st.runs())
	}
	if err := w.bob.Accept(task.ID); err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, task.ID)
	eventually(t, "alice's view settled by the result", func() bool {
		e := convExec(t, w.alice, conv, task.LID)
		return e != nil && e.State == envelope.StatusDone && e.Host == w.bob.Address
	})

	// The requester's own status about its request is held at the host,
	// never kept.
	body, _ := json.Marshal(envelope.Status{State: "running", N: 99, At: time.Now().Unix()})
	forged, err := w.alice.sendControlAs(tctx(t), ControlRef{Conv: conv, ID: task.LID, Fingerprint: w.alice.Self().Fingerprint()}, envelope.SubStatus, string(body), protocol.CapHeadless)
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold alice's status", func() bool { return quarantined(t, w.bob, forged.ID) })
	if n := inboxCount(t, w.bob, `sub = ? AND sender = ?`, envelope.SubStatus, w.alice.Address); n != 0 {
		t.Fatalf("bob kept %d status(es) from the requester", n)
	}

	// A request whose participation ended is closed by the look, and told.
	if _, err := w.alice.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the dismissal", func() bool { return stateAt(t, w.bob, pid).State == PartDismissed })
	stopBob()
	in := agentRequest(t, w, conv, pid, envelope.KindTask)
	if _, err := w.bob.store.addConvInbox(in, w.alice.Self().Fingerprint(), stateAgentWaiting, false, nil); err != nil {
		t.Fatal(err)
	}
	_, found, _, _, told, err := w.bob.store.claimAgentPageTold("agentstub", w.bob.Address, w.bob.Self().Fingerprint(), 0, agentPage)
	if err != nil || found || !slices.Contains(told, in.ID) || jobState(t, w.bob, in.ID) != stateNotRun {
		t.Fatalf("ended request: found %v, told %v, state %q, %v", found, told, jobState(t, w.bob, in.ID), err)
	}
}
