package client

import (
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestClaimJobIdleThenNewAuthorizedRequest(t *testing.T) {
	w := newWorld(t, "")
	claimNone := func() {
		t.Helper()
		j, found, err := w.bob.store.claimJob("fixture")
		if err != nil || found {
			t.Fatalf("unexpected idle claim: %s %v %v", j.ID, found, err)
		}
	}
	claimNone()
	// Configure a responder for acceptance, but never start a worker or
	// invoke a harness: this test exercises admission and claiming only.
	if err := w.bob.SetResponder(&Responder{Harness: "codex", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if err := w.bob.Approve(w.alice.Address); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{envelope.KindQuestion, envelope.KindTask} {
		env := sealTo(t, w.alice, w.bob, envelope.Inner{Kind: kind, Body: "new request after idle wake"})
		if err := w.bob.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		if kind == envelope.KindTask {
			// Question approval grants no task authority. The pending task
			// remains unclaimed until the recipient explicitly accepts it.
			claimNone()
			if err := w.bob.Accept(env.ID); err != nil {
				t.Fatal(err)
			}
		}
		j, found, err := w.bob.store.claimJob("fixture")
		if err != nil || !found || j.ID != env.ID || jobState(t, w.bob, env.ID) != stateRunning {
			t.Fatalf("new authorized request not claimed exactly: %s want %s found=%v err=%v", j.ID, env.ID, found, err)
		}
		claimNone()
	}
}
