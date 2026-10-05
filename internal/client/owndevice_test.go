package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// "My devices are me" (owner rule 2026-10-05): a task from another current
// device of this person to this device's agent runs without asking, as the
// person's own, and the worker claims it (D9 for device threads). Someone
// else's task still waits; untrusting the device, or removing it from the
// roster, makes its tasks wait for the OK again.
func TestOwnTrustedDeviceTaskRunsAndIsClaimed(t *testing.T) {
	st := installStub(t, "answer")
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice)
	setResponder(t, w.alice, "stub", st.dir, time.Minute)
	desk := linkedVia(t, w.alice, "desk", approveLink(w.alice))
	task, err := desk.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "tidy the build folder", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, task.ID, stateAnswered)
	eventually(t, "the result on the desk", func() bool { r, ok := findReply(desk, task.ID); return ok && r.Kind == envelope.KindResult })

	other, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "someone else's task", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, other.ID, stateAwaiting)

	if err := w.alice.UntrustOwnDevice(desk.Address); err != nil {
		t.Fatal(err)
	}
	again, err := desk.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "and the cache", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, again.ID, stateAwaiting)
	if st.count() != 1 {
		t.Fatalf("ran %d time(s)", st.count())
	}
}

// A device-thread task pending to run from an own device waits again when
// the person untrusts that device; the predicate is the one SQL form for
// insertion and claim.
func TestUntrustDemotesDeviceTask(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice)
	desk := linkedVia(t, w.alice, "desk", approveLink(w.alice))
	// No responder here: the task stays pending to run, as the person's own.
	task, err := desk.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: "rotate the logs", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.alice, task.ID, statePending)
	if ok, err := ownDeviceHolds(w.alice.store.db, desk.Address, desk.Self().Fingerprint()); err != nil || !ok {
		t.Fatalf("own device %v %v", ok, err)
	}
	if ok, _ := ownDeviceHolds(w.alice.store.db, desk.Address, w.bob.Self().Fingerprint()); ok {
		t.Fatal("a key the roster does not list holds")
	}
	if err := w.alice.UntrustOwnDevice(desk.Address); err != nil {
		t.Fatal(err)
	}
	m := inboxRow(t, w.alice, task.ID)
	if m.State != stateAwaiting || !strings.Contains(m.Detail, "untrusted") {
		t.Fatalf("after untrust %+v", m)
	}
	if ok, _ := ownDeviceHolds(w.alice.store.db, desk.Address, desk.Self().Fingerprint()); ok {
		t.Fatal("an untrusted device still holds")
	}
	if err := w.alice.UntrustOwnDevice("nobody/none"); err == nil {
		t.Fatal("untrusted a device that is not one of yours")
	}
}
