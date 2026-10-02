package client

import (
	"errors"
	"os"
	"testing"
)

func TestClaudeReplySharedTakeExactReceiptAndGeneration(t *testing.T) {
	w := newWorld(t, "")
	owner, route := claudeReceiverFixture(t, w.alice)
	binding, input := nativeInput(t, w.alice, w.bob, owner.Handle)
	d, e := w.alice.TakeReplyReceiverInput(owner.ReplySessionCall)
	if e != nil || d == nil || d.BindingID != binding || d.InputID != input || d.ReconcileOnly {
		t.Fatalf("lazy native take %+v %v", d, e)
	}
	again, e := w.alice.TakeReplyReceiverInput(owner.ReplySessionCall)
	if e != nil || again == nil || !again.ReconcileOnly || again.InputToken != d.InputToken {
		t.Fatalf("uncertain claim changed %+v %v", again, e)
	}
	ack := ReplyReceiverAck{ReplySessionCall: owner.ReplySessionCall, BindingID: binding, InputID: input, ClaimID: d.ClaimID, InputToken: d.InputToken}
	if ok, e := w.alice.AckReplyReceiverInput(ack); !errors.Is(e, os.ErrNotExist) || ok {
		t.Fatalf("notification without physical receipt accepted %v %v", ok, e)
	}
	wrong := claudeReceiptRow(owner.SessionID, owner.Source, ack)
	wrong["isMeta"] = false
	writeClaudeRows(t, owner.File, wrong)
	if ok, e := w.alice.AckReplyReceiverInput(ack); e != nil || ok {
		t.Fatalf("ordinary user echo accepted %v %v", ok, e)
	}
	writeClaudeRows(t, owner.File, claudeReceiptRow(owner.SessionID, owner.Source, ack))
	for range 2 {
		if ok, e := w.alice.AckReplyReceiverInput(ack); e != nil || !ok {
			t.Fatalf("exact physical native token not accepted %v %v", ok, e)
		}
	}
	if next, e := w.alice.TakeReplyReceiverInput(owner.ReplySessionCall); e != nil || next != nil {
		t.Fatalf("accepted input redelivered %+v %v", next, e)
	}
	if _, e = w.alice.registerClaudeReplySession("SessionStart", owner.SessionID, owner.File, route); e != nil {
		t.Fatal(e)
	}
	if ok, e := w.alice.AckReplyReceiverInput(ack); e == nil || ok {
		t.Fatalf("old generation accepted %v %v", ok, e)
	}
	if _, ok, e := w.alice.store.claimJob("stub"); e != nil || ok {
		t.Fatalf("default stole native data %v %v", ok, e)
	}
}
