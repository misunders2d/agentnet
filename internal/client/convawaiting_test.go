package client

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
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

// A conversation request takes no remote decision: a status answering a
// decision this device sent there about a device-thread request of the
// same id is that thread's, never one for a conversation request.
func TestConvStatusTakesNoRemoteDecision(t *testing.T) {
	w := newWorld(t, "")
	request, decision, fp := protocol.NewID(), protocol.NewID(), w.alice.Self().Fingerprint()
	sent, _ := json.Marshal(envelope.Decision{Action: "accept", Expect: "awaiting", Attempt: 1, Report: "r"})
	if _, err := w.alice.store.db.Exec(`INSERT INTO outbox(id, recipient, body, envelope, state, created_at, sub, ref_id, ref_fp) VALUES(?, ?, ?, '{}', 'delivered', ?, ?, ?, ?)`,
		decision, w.bob.Address, string(sent), time.Now().Unix(), envelope.SubDecision, request, fp); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(envelope.Status{State: "running", Decision: decision, Report: "r", Attempt: 1})
	status := envelope.Inner{Body: string(body), Ref: &envelope.Ref{ID: request, Fingerprint: fp}}
	if ok, why := w.alice.statusAllowed(status, w.bob.Address, w.bob.Self().Fingerprint()); !ok {
		t.Fatalf("the answer to that decision in its device thread refused: %s", why)
	}
	status.Conv = protocol.NewID()
	if ok, _ := w.alice.statusAllowed(status, w.bob.Address, w.bob.Self().Fingerprint()); ok {
		t.Fatal("a conversation status let through by a device-thread decision")
	}
}

// A status carried as history is held to the same rule as a direct one:
// only the device the request is for speaks for it. Alice's new phone
// keeps the status Bob's device, which her task is for, sent about it, and
// holds as invalid a status her laptop forwards as its own about that task.
func TestHistoryCarriedStatusOnlyFromTheRequestsDevice(t *testing.T) {
	w, conv, _, _ := agentWorld(t)
	pid := participate(t, w, conv, nil, nil)
	task, err := w.alice.AskAgent(tctx(t), pid, envelope.KindTask, "restart the deploy")
	if err != nil {
		t.Fatal(err)
	}
	phone := linked(t, w.alice)
	eventually(t, "the phone has the task", func() bool { _, ok := findLID(t, phone, conv, task.LID); return ok })
	me, _, _ := w.alice.store.selfPerson(w.alice.Address)
	var phoneDev identity.Public
	for _, d := range me.roster.Devices {
		if d.Address == phone.Address {
			phoneDev = d
		}
	}
	_, raw, _, _ := w.alice.store.conversation(conv)
	forward := func(from string, key identity.Public) envelope.Inner {
		t.Helper()
		body, _ := json.Marshal(envelope.Status{State: "running", N: 7, At: time.Now().Unix()})
		st := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: from, TS: time.Now().Unix(), Kind: envelope.KindMessage,
			Sub: envelope.SubStatus, Body: string(body), Conv: conv, LID: protocol.NewID(),
			Ref: &envelope.Ref{ID: task.LID, Fingerprint: w.alice.Self().Fingerprint()}}
		c, err := w.alice.historyCopy(phoneDev, conv, raw, itemOf(st, key.Fingerprint(), time.Now().UnixMilli()))
		if err != nil {
			t.Fatal(err)
		}
		tx, _ := w.alice.store.db.Begin()
		if err := insertCopies(tx, []outCopy{c}); err != nil {
			t.Fatal(err)
		}
		tx.Commit()
		if _, err := w.alice.deliver(tctx(t), c.env, nil); err != nil {
			t.Fatal(err)
		}
		st.ID = c.env.ID // the carrier, as the phone holds it
		return st
	}
	heldInvalid := func(id string) bool {
		var n int
		phone.store.db.QueryRow(`SELECT count(*) FROM quarantine WHERE id = ? AND reason = ?`, id, reasonInvalid).Scan(&n)
		return n == 1
	}
	kept := forward(w.bob.Address, w.bob.Self())
	eventually(t, "the phone to keep the status of the task's device", func() bool { return controlRows(t, phone, kept.LID) == 1 })
	forged := forward(w.alice.Address, w.alice.Self())
	eventually(t, "the phone to decide on the laptop's own status", func() bool { return heldInvalid(forged.ID) || controlRows(t, phone, forged.LID) != 0 })
	if n := controlRows(t, phone, forged.LID); n != 0 || !heldInvalid(forged.ID) {
		t.Fatalf("the laptop's own status about the task was stored (%d), not held as invalid", n)
	}
}
