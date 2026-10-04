package client

import (
	"errors"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// A request to this device's agent that waits for its person's accept can
// be declined instead, by the same requests Accept takes (received, not a
// replica, a question or task of a participation, waiting for that accept).
// It is the person's decision, not an agent's turn: nothing goes to the
// conversation, the reason stays with the request here, and the requester
// is told by the request's status, also once the participation ended;
// nothing runs it then or later, an accept included. Anything else is not
// declined: a request already answered, a question for the person
// (answered in the conversation), a replica, and the requester's own copy.
func TestDeclineConversationRequest(t *testing.T) {
	st := installAgentStub(t)
	w, conv, _, _ := agentWorld(t)
	setResponder(t, w.bob, "agentstub", st.dir, time.Minute)
	pid := participate(t, w, conv, nil, nil)
	task, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "restart the deploy")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, task.ID, stateAwaiting)

	// Refused: the requester's own copy is no request to decline here.
	if _, err := w.alice.Decline(tctx(t), task.ID, "no"); err == nil {
		t.Fatal("the requester declined its own request")
	}

	res, err := w.bob.Decline(tctx(t), task.ID, "not today")
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != task.ID || res.State != stateDeclined || jobState(t, w.bob, task.ID) != stateDeclined {
		t.Fatalf("decline: %+v, state %q", res, jobState(t, w.bob, task.ID))
	}
	if m, _ := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.ID == task.ID }); m.Detail != "not today" {
		t.Fatalf("the reason kept with the request: %q", m.Detail)
	}
	eventually(t, "alice's view says declined", func() bool {
		e := convExec(t, w.alice, conv, task.LID)
		return e != nil && e.State == envelope.StatusDeclined && e.Host == w.bob.Address
	})
	if err := w.bob.Accept(task.ID); !errors.Is(err, ErrNotPending) {
		t.Fatalf("a declined request accepted: %v", err)
	}
	if _, err := w.bob.Decline(tctx(t), task.ID, "again"); err == nil {
		t.Fatal("declined twice")
	}
	noReply(t, w.alice, conv, task.ID)
	if st.runs() != 0 || jobState(t, w.bob, task.ID) != stateDeclined {
		t.Fatalf("a declined request ran (%d) or changed (%q)", st.runs(), jobState(t, w.bob, task.ID))
	}

	// Refused: a question that is already answered.
	q, err := w.alice.AskAgent(tctx(t), pid, envelope.KindQuestion, "what failed?")
	if err != nil {
		t.Fatal(err)
	}
	replyAt(t, w.alice, conv, q.ID)
	if _, err := w.bob.Decline(tctx(t), q.ID, "no"); err == nil {
		t.Fatal("an answered question declined")
	}

	// Refused: a question for bob himself, answered in the conversation.
	held, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Kind: envelope.KindQuestion, Body: "lunch?"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, held.ID, stateConvHeld)
	if _, err := w.bob.Decline(tctx(t), held.ID, "no"); !errors.Is(err, ErrConversationItem) {
		t.Fatalf("a question for the person declined like an agent's: %v", err)
	}

	// Refused: a replica of a request waiting for an accept.
	in := agentRequest(t, w, conv, pid, envelope.KindTask)
	in.Replica = true
	if _, err := w.bob.store.addConvInbox(in, w.alice.Self().Fingerprint(), stateAwaiting, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := w.bob.Decline(tctx(t), in.ID, "no"); err == nil || jobState(t, w.bob, in.ID) != stateAwaiting {
		t.Fatalf("a replica declined: %v, state %q", err, jobState(t, w.bob, in.ID))
	}
	if st.runs() != 1 {
		t.Fatalf("runs %d, want 1 (the answered question)", st.runs())
	}

	// Declined after its participation ended: told all the same.
	late, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "rotate the logs")
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, late.ID, stateAwaiting)
	if _, err := w.alice.DismissParticipation(tctx(t), pid); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to see the dismissal", func() bool { return stateAt(t, w.bob, pid).State == PartDismissed })
	if _, err := w.bob.Decline(tctx(t), late.ID, ""); err != nil || jobState(t, w.bob, late.ID) != stateDeclined {
		t.Fatalf("decline after the end: %v, state %q", err, jobState(t, w.bob, late.ID))
	}
	eventually(t, "alice's view of the late task says declined", func() bool {
		e := convExec(t, w.alice, conv, late.LID)
		return e != nil && e.State == envelope.StatusDeclined
	})
	if st.runs() != 1 {
		t.Fatalf("runs %d after the late decline", st.runs())
	}
}
