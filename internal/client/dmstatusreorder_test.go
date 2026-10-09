package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestDMOwnTargetStatusReorderedBeforeOriginal(t *testing.T) {
	w, conv, _, stopBob := agentWorld(t)
	p := p6Member(t, w.alice, w.alice, conv)
	eventually(t, "observer knows exact own target", func() bool { return stateAt(t, w.bob, p.PID).Claimable() })
	stopBob()
	request := agentRequest(t, w, conv, p.PID, envelope.KindQuestion)
	request.Target = &envelope.Target{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}
	request.Body = "synthetic own-target request"
	original := craft(t, w.alice, w.bob, request)
	body, _ := json.Marshal(envelope.Status{State: "running", N: 1, At: time.Now().Unix()})
	status := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: w.alice.Address, To: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubStatus, Body: string(body), Conv: conv, LID: protocol.NewID(), Ref: &envelope.Ref{ID: request.LID, Fingerprint: w.alice.Self().Fingerprint()}}
	rec, e := w.bob.Self().Recipient()
	if e != nil {
		t.Fatal(e)
	}
	env, e := envelope.Seal(status, w.alice.id.Sign, rec)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.bob.verifyAndStore(tctx(t), env); e != nil {
		t.Fatal(e)
	}
	historyRecoveryReason(t, w.bob, env.ID, reasonProof)
	if inboxCount(t, w.bob, `id=?`, env.ID) != 0 {
		t.Fatal("early DM status admitted without request")
	}
	if _, e = w.bob.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	if e = w.bob.verifyAndStore(tctx(t), original); e != nil {
		t.Fatal(e)
	}
	w.bob.retryProof(tctx(t))
	if inboxCount(t, w.bob, `id=?`, env.ID) != 0 {
		t.Fatal("fresh DM status admitted under pending executor key")
	}
	if _, e = w.bob.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address); e != nil {
		t.Fatal(e)
	}
	w.bob.retryProof(tctx(t))
	if inboxCount(t, w.bob, `id=? AND sub=?`, env.ID, envelope.SubStatus) != 1 {
		t.Fatal("same DM status did not recover after original")
	}
	wrong := status
	wrong.ID, wrong.LID = protocol.NewID(), protocol.NewID()
	wrong.From = w.bob.Address
	forged, e := envelope.Seal(wrong, w.bob.id.Sign, rec)
	if e != nil {
		t.Fatal(e)
	}
	if e = w.bob.verifyAndStore(tctx(t), forged); e != nil {
		t.Fatal(e)
	}
	historyRecoveryReason(t, w.bob, forged.ID, reasonInvalid)
}
