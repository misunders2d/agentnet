package client

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func remoteReceiverWorld(t *testing.T) (*world, *Agent, func(), func()) {
	t.Helper()
	w := newWorld(t, "")
	stopHost := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	req := pendingLink(t, w.alice)
	if e := w.alice.DecideLink(tctx(t), req.ID, true); e != nil {
		t.Fatal(e)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	stopPhone := runAgent(t, phone)
	eventually(t, "normal receiver cap", func() bool {
		var p protocol.Profile
		e := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+w.alice.Address+"/profile", nil, &p)
		return e == nil && p.Supports(w.alice.Address, w.alice.Self().SignKey, protocol.CapReplyReceiver)
	})
	return w, phone, stopPhone, stopHost
}
func remoteReceiverSend(t *testing.T, w *world, phone *Agent, r ReplyReceiver, files ...string) (SendResult, string) {
	t.Helper()
	r.Host = &ReplyReceiverHost{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}
	if _, e := w.alice.sendKey(tctx(t), phone.Address); e != nil {
		t.Fatal(e)
	}
	if _, e := w.alice.GrantTasks(phone.Address); e != nil {
		t.Fatal(e)
	}
	sent, e := phone.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "PHONE_AUTHORIZED_ORIGINAL", ReplyReceiver: &r, Files: files})
	if e != nil {
		t.Fatal(e)
	}
	origin := receiverBindings(t, phone)
	var setup string
	for _, b := range origin {
		if b.RequestRef == sent.ID {
			full, e := replyReceiverIn(phone.store.db, b.ID)
			if e != nil {
				t.Fatal(e)
			}
			setup = full.remote.Route.DelegationID
		}
	}
	if setup == "" {
		t.Fatal("origin commitment missing")
	}
	eventually(t, "standing local grant releases exact original", func() bool {
		var n int
		w.bob.store.db.QueryRow(`SELECT count(*) FROM inbox WHERE id=?`, sent.ID).Scan(&n)
		return n == 1
	})
	return sent, setup
}
func saveRemoteReceiverVectors(t *testing.T, w *world, phone *Agent, sent SendResult, setup string) {
	t.Helper()
	dir := os.Getenv("AGENTNET_RECEIVER_VECTOR_DIR")
	if dir == "" {
		return
	}
	type vector struct{ Original, Delegate, Ready envelope.Inner }
	var v vector
	for _, item := range []struct {
		at, to *Agent
		id     string
		dst    *envelope.Inner
	}{{phone, w.bob, sent.ID, &v.Original}, {phone, w.alice, setup, &v.Delegate}} {
		var raw string
		if e := item.at.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, item.id).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		var env envelope.Envelope
		if e := json.Unmarshal([]byte(raw), &env); e != nil {
			t.Fatal(e)
		}
		in, e := envelope.Open(env, item.to.id, item.to.Address, item.at.Self())
		if e != nil {
			t.Fatal(e)
		}
		*item.dst = in
	}
	var raw string
	if e := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE reply_to=? AND required_cap=? ORDER BY created_at LIMIT 1`, setup, protocol.CapReplyReceiver).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var env envelope.Envelope
	if e := json.Unmarshal([]byte(raw), &env); e != nil {
		t.Fatal(e)
	}
	ready, e := envelope.Open(env, phone.id, phone.Address, w.alice.Self())
	if e != nil {
		t.Fatal(e)
	}
	v.Ready = ready
	bytes, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "native-"+t.Name()+".json"), append(bytes, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestReceiverRemoteManagedContinuationAfterPhoneStops(t *testing.T) {
	stub := installAgentStub(t)
	w, phone, stopPhone, _ := remoteReceiverWorld(t)
	chosen, e := w.alice.CreateLocalAgent("selected A", Responder{Harness: "agentstub", Dir: stub.dir})
	if e != nil {
		t.Fatal(e)
	}
	if e = w.alice.SetResponder(&Responder{Harness: "agentstub", Dir: stub.dir}); e != nil {
		t.Fatal(e)
	}
	path, content := writeFile(t, t.TempDir(), "authorized.txt", 83)
	r := ReplyReceiver{Kind: "managed_agent", AgentID: chosen.ID, Instructions: "Continue explicitly authorized plan A using exact original transcript and selected files.", Mode: envelope.KindQuestion}
	sent, setup := remoteReceiverSend(t, w, phone, r, path)
	saveRemoteReceiverVectors(t, w, phone, sent, setup)
	stopPhone()
	response, e := w.bob.SendMessage(tctx(t), Outgoing{To: phone.Address, Kind: envelope.KindQuestion, Body: "CORRELATED_REMOTE_DATA", ReplyTo: sent.ID})
	if e != nil {
		t.Fatal(e)
	}
	var input string
	eventually(t, "chosen A completes selected clarification data", func() bool {
		w.alice.store.db.QueryRow(`SELECT inbox_id FROM reply_receiver_inputs WHERE binding=?`, setup).Scan(&input)
		if input == "" {
			return false
		}
		state, e := w.alice.store.jobState(input)
		return e == nil && state == stateContinued
	})
	if input == response.ID {
		t.Fatal("fanout physical IDs reused")
	}
	if stub.runs() != 1 {
		t.Fatalf("selected/default duplicate runs%d", stub.runs())
	}
	if !strings.Contains(stub.last(), r.Instructions) || !strings.Contains(stub.last(), "PHONE_AUTHORIZED_ORIGINAL") || !strings.Contains(stub.last(), "CORRELATED_REMOTE_DATA") || !strings.Contains(stub.last(), "Verified attachment") {
		t.Fatal("selected continuation lost original authority/files/data")
	}
	var raw string
	if e = w.alice.store.db.QueryRow(`SELECT executor FROM inbox WHERE id=?`, input).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var stamp ExecutorStamp
	if e = json.Unmarshal([]byte(raw), &stamp); e != nil || stamp.AgentID != chosen.ID {
		t.Fatalf("wrong chosen executor %+v %v", stamp, e)
	}
	stream, _, e := w.alice.OpenFileFrom(tctx(t), "in", setup, 0)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(readAll(t, stream), content) {
		t.Fatal("original delegate file changed")
	}
	if j, claimed, e := w.alice.store.claimJob("agentstub"); e != nil || claimed {
		t.Fatalf("default claimed selected data %+v %t %v", j, claimed, e)
	}
}
func TestReceiverRemoteNativeClaimRestartReceipt(t *testing.T) {
	w, phone, stopPhone, stopHost := remoteReceiverWorld(t)
	owner, call := nativeReceiverFixture(t, w.alice, "omp")
	sent, setup := remoteReceiverSend(t, w, phone, ReplyReceiver{Kind: "live_session", SessionHandle: owner.Handle})
	saveRemoteReceiverVectors(t, w, phone, sent, setup)
	stopPhone()
	if _, e := w.bob.Reply(tctx(t), sent.ID, "NATIVE_REMOTE_DATA"); e != nil {
		t.Fatal(e)
	}
	eventually(t, "native selected input", func() bool {
		var n int
		w.alice.store.db.QueryRow(`SELECT count(*) FROM reply_receiver_inputs WHERE binding=?`, setup).Scan(&n)
		return n == 1
	})
	delivery, e := w.alice.TakeReplyReceiverInput(call)
	if e != nil || delivery == nil || delivery.RequestBody != "PHONE_AUTHORIZED_ORIGINAL" || delivery.ReconcileOnly {
		t.Fatalf("native take %+v %v", delivery, e)
	}
	nativeWrite(t, call.File, call.SessionID, map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "details": map[string]string{"handle": owner.Handle}}, nativeEntry(delivery, "accepted", "marker"))
	stopHost()
	home := w.alice.home
	w.alice.Close()
	reopened, e := Open(home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	registration, e := reopened.RegisterReplySession(ReplySessionRegistration{Harness: "omp", SessionID: call.SessionID, File: call.File, Leaf: "accepted", Handle: call.Handle})
	if e != nil {
		t.Fatal(e)
	}
	call.Generation, call.OwnerToken, call.Leaf = registration.Generation, registration.OwnerToken, "accepted"
	again, e := reopened.TakeReplyReceiverInput(call)
	if e != nil || again == nil || !again.ReconcileOnly || again.InputToken != delivery.InputToken {
		t.Fatalf("restart repeated delivery %+v %v", again, e)
	}
	if accepted, e := reopened.AckReplyReceiverInput(ReplyReceiverAck{ReplySessionCall: call, BindingID: setup, InputID: delivery.InputID, ClaimID: delivery.ClaimID, InputToken: delivery.InputToken}); e != nil || !accepted {
		t.Fatalf("native physical ACK %t %v", accepted, e)
	}
	if again, e := reopened.TakeReplyReceiverInput(call); e != nil || again != nil {
		t.Fatalf("accepted input redispatched %+v %v", again, e)
	}
	if j, claimed, e := reopened.store.claimJob("default"); e != nil || claimed {
		t.Fatalf("default stole native input %+v %t %v", j, claimed, e)
	}
}
