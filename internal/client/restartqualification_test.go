package client

import (
	"github.com/misunders2d/agentnet/internal/envelope"
	"sync/atomic"
	"testing"
	"time"
)

func TestSameVersionQualificationDoesNotFenceOrRestartJobs(t *testing.T) {
	setVersion(t, "v9.9.8")
	st := installStub(t, "slow")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	exe := fakeProgram(t, "v9.9.8")
	var switchChecks atomic.Int32
	result, _ := runUntilStop(t, w.bob, RunOptions{Executable: exe, CanSwitch: func() (bool, string) { switchChecks.Add(1); return false, "must not be called for qualification" }})
	runWith(t, w, w.alice, RunOptions{})
	first, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "active qualification fixture", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, first.ID, stateAwaiting)
	if err := w.bob.Accept(first.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, first.ID, stateRunning)
	for _, request := range []UpdateRequest{{ID: "wrong-qualified-target", Exe: exe + "-other", To: "v9.9.8"}, {ID: "same-version-qualified-target", Exe: exe, To: "v9.9.8"}} {
		if err := RequestUpdateSwitch(w.bobHome, request); err != nil {
			t.Fatal(err)
		}
		eventually(t, "qualification activation", func() bool { act, ok, _ := ReadUpdateActivation(w.bobHome); return ok && act.ID == request.ID })
		act := activation(t, w.bob)
		want := ActivationRunning
		if request.ID == "wrong-qualified-target" {
			want = ActivationNotApplied
		}
		if act.Result != want || act.Running != "v9.9.8" || act.PID <= 0 {
			t.Fatalf("activation: %+v", act)
		}
		if w.bob.updatePending() != nil || w.bob.UpdateSwitching() != nil || switchChecks.Load() != 0 {
			t.Fatal("identity qualification entered the job fence/restart path")
		}
	}
	second, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "after qualification", Kind: envelope.KindTask})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, second.ID, stateAwaiting)
	if err := w.bob.Accept(second.ID); err != nil {
		t.Fatal(err)
	}
	waitState(t, w.bob, first.ID, stateAnswered)
	waitState(t, w.bob, second.ID, stateAnswered)
	if !resultStored(t, w.bob, first.ID) || !resultStored(t, w.bob, second.ID) || st.count() != 2 {
		t.Fatal("qualification lost/repeated a running or subsequently accepted job")
	}
	select {
	case err := <-result:
		t.Fatalf("daemon stopped for identity qualification: %v", err)
	default:
	}
}
