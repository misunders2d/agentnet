package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// A reader holds an executor's status that came before the request it names
// (proof pending). The request's arrival looks at held messages again, so
// the status is admitted on the next sync instead of waiting for unrelated
// evidence (a members push), as in the browser engine: the topic reads the
// executor's word, not "unconfirmed". With nothing held, nothing is looked at.
func TestStatusHeldBeforeItsRequestIsAdmittedWithIt(t *testing.T) {
	w, conv, _, stopBob := agentWorld(t)
	p := p6Member(t, w.alice, w.alice, conv)
	eventually(t, "observer knows exact own target", func() bool { return stateAt(t, w.bob, p.PID).Claimable() })
	stopBob() // syncs below are this test's, one at a time
	ask := func() (envelope.Inner, envelope.Envelope) {
		request := agentRequest(t, w, conv, p.PID, envelope.KindQuestion)
		request.Target = &envelope.Target{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}
		request.Body = "own-target request"
		return request, craft(t, w.alice, w.bob, request)
	}
	_, quietEnv := ask()
	w.bob.convWork.take()
	if err := w.bob.verifyAndStore(tctx(t), quietEnv); err != nil {
		t.Fatal(err)
	}
	if w.bob.convWork.take()&convRetry != 0 {
		t.Fatal("a request looked at held messages again with nothing held")
	}
	request, original := ask()
	body, _ := json.Marshal(envelope.Status{State: "queued", N: 1, At: time.Now().Unix(), Detail: "waiting here until it may run"})
	status := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: string(body), Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: request.LID, Fingerprint: w.alice.Self().Fingerprint()}}
	rec, err := w.bob.Self().Recipient()
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Seal(status, w.alice.id.Sign, rec)
	if err != nil {
		t.Fatal(err)
	}
	if err = w.bob.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	historyRecoveryReason(t, w.bob, env.ID, reasonProof)
	w.bob.convWork.take() // no other evidence comes meanwhile
	if err = w.bob.verifyAndStore(tctx(t), original); err != nil {
		t.Fatal(err)
	}
	w.bob.convSync(tctx(t))
	if inboxCount(t, w.bob, `id=? AND sub=?`, env.ID, envelope.SubStatus) != 1 {
		t.Fatal("the status stayed held after its request arrived")
	}
	m, _ := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == request.LID })
	if m.Exec == nil || m.Exec.State != "queued" {
		t.Fatalf("the request does not carry its executor's word: %+v", m.Exec)
	}
}
