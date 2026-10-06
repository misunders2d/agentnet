package client

import (
	"context"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestAppUpdatePauseRefusesRunningAndFencesQueuedJobs(t *testing.T) {
	st := installStub(t, "slow")
	w := newWorld(t, "")
	setResponder(t, w.bob, "stub", st.dir, time.Minute)
	runWith(t, w, w.bob, RunOptions{})
	runWith(t, w, w.alice, RunOptions{})
	sent, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindTask, Body: "answer"})
	if e != nil {
		t.Fatal(e)
	}
	waitState(t, w.bob, sent.ID, stateAwaiting)
	if e = w.bob.Accept(sent.ID); e != nil {
		t.Fatal(e)
	}
	waitState(t, w.bob, sent.ID, stateRunning)
	if resume, e := w.bob.PauseForAppUpdate(); e == nil {
		resume()
		t.Fatal("running job allowed update")
	}
	waitState(t, w.bob, sent.ID, stateAnswered)
	var resume func()
	deadline := time.Now().Add(2 * time.Second)
	for {
		resume, e = w.bob.PauseForAppUpdate()
		if e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal(e)
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer resume()
	if w.bob.runNext(context.Background(), w.bob.workerWake) {
		t.Fatal("job claim crossed app update fence")
	}
}
