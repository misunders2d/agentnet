package client

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func nativeWrite(t *testing.T, file, sid string, entries ...map[string]any) {
	t.Helper()
	rows := []map[string]any{{"type": "session", "id": sid, "version": 3}}
	rows = append(rows, entries...)
	var data []byte
	for _, row := range rows {
		raw, e := json.Marshal(row)
		if e != nil {
			t.Fatal(e)
		}
		data = append(data, raw...)
		data = append(data, '\n')
	}
	if e := os.WriteFile(file, data, 0600); e != nil {
		t.Fatal(e)
	}
}
func nativeReceiverFixture(t *testing.T, a *Agent, harness string) (ReplySessionOwner, ReplySessionCall) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "native.jsonl")
	sid := protocol.NewID()
	nativeWrite(t, file, sid)
	r, e := a.RegisterReplySession(ReplySessionRegistration{Harness: harness, SessionID: sid, File: file})
	if e != nil {
		t.Fatal(e)
	}
	nativeWrite(t, file, sid, map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "details": map[string]string{"handle": r.Handle}})
	return r, ReplySessionCall{Handle: r.Handle, Generation: r.Generation, OwnerToken: r.OwnerToken, SessionID: sid, File: file, Leaf: "marker"}
}
func nativeInput(t *testing.T, a, b *Agent, handle string) (string, string) {
	t.Helper()
	r := ReplyReceiver{Kind: "live_session", SessionHandle: handle}
	sent, e := a.SendMessage(tctx(t), Outgoing{To: b.Address, Kind: envelope.KindQuestion, Body: "original local plan", ReplyReceiver: &r})
	if e != nil {
		t.Fatal(e)
	}
	reply := receiverDirect(t, b, a, envelope.Inner{Kind: envelope.KindQuestion, Body: "correlated clarification data", ReplyTo: sent.ID})
	if e = a.verifyAndStore(tctx(t), reply); e != nil {
		t.Fatal(e)
	}
	rows := receiverBindings(t, a)
	for _, v := range rows {
		if v.RequestRef == sent.ID {
			if len(v.Inputs) != 1 {
				t.Fatalf("inputs %+v", v)
			}
			return v.ID, reply.ID
		}
	}
	t.Fatal("missing binding")
	return "", ""
}
func nativeEntry(d *ReplyReceiverDelivery, id, parent string) map[string]any {
	return map[string]any{"type": "message", "id": id, "parentId": parent, "message": map[string]any{"role": "custom", "customType": "agentnet-receiver", "details": map[string]string{"binding_id": d.BindingID, "input_id": d.InputID, "claim_id": d.ClaimID, "input_token": d.InputToken}}}
}
func TestReplySessionClaimDurabilityAndNativeAck(t *testing.T) {
	w := newWorld(t, "")
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	binding, input := nativeInput(t, w.alice, w.bob, r.Handle)
	d, e := w.alice.TakeReplyReceiverInput(call)
	if e != nil || d == nil || d.ReconcileOnly || d.BindingID != binding || d.InputID != input || d.RequestBody != "original local plan" {
		t.Fatalf("take %+v %v", d, e)
	}
	second, e := w.alice.TakeReplyReceiverInput(call)
	if e != nil || !second.ReconcileOnly || second.InputToken != d.InputToken {
		t.Fatalf("duplicate %+v %v", second, e)
	}
	ack := ReplyReceiverAck{ReplySessionCall: call, BindingID: binding, InputID: input, ClaimID: d.ClaimID, InputToken: d.InputToken}
	if ok, e := w.alice.AckReplyReceiverInput(ack); e != nil || ok {
		t.Fatalf("API return isn't native persistence %v %v", ok, e)
	}
	marker := map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "details": map[string]string{"handle": r.Handle}}
	wrong := nativeEntry(d, "wrong", "marker")
	wrong["message"].(map[string]any)["details"].(map[string]string)["binding_id"] = "other"
	nativeWrite(t, call.File, call.SessionID, marker, wrong)
	ack.Leaf = "wrong"
	if ok, e := w.alice.AckReplyReceiverInput(ack); e != nil || ok {
		t.Fatalf("wrong binding ACK %v %v", ok, e)
	}
	nativeWrite(t, call.File, call.SessionID, marker, nativeEntry(d, "accepted", "marker"))
	ack.Leaf = "accepted"
	for range 2 {
		if ok, e := w.alice.AckReplyReceiverInput(ack); e != nil || !ok {
			t.Fatalf("durable ACK %v %v", ok, e)
		}
	}
	rows := receiverBindings(t, w.alice)
	if rows[0].Inputs[0].State != "accepted" {
		t.Fatalf("not acceptance %+v", rows)
	}
	if got, e := w.alice.TakeReplyReceiverInput(ack.ReplySessionCall); e != nil || got != nil {
		t.Fatalf("accepted redispatched %+v %v", got, e)
	}
	if e := w.alice.Approve(w.bob.Address); e != nil {
		t.Fatal(e)
	}
	if job, ok, e := w.alice.store.claimJob("stub"); e != nil || ok {
		t.Fatalf("approved default stole selected data %+v %v", job, e)
	}
	independent := receiverDirect(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindQuestion, Body: "independent approved question"})
	if e := w.alice.verifyAndStore(tctx(t), independent); e != nil {
		t.Fatal(e)
	}
	if job, ok, e := w.alice.store.claimJob("stub"); e != nil || !ok || job.ID != independent.ID {
		t.Fatalf("independent default permission changed %+v %v %v", job, ok, e)
	}
	if _, e := w.alice.RegisterReplySession(ReplySessionRegistration{Harness: "pi", SessionID: call.SessionID, File: call.File, Leaf: "marker", Handle: r.Handle}); e == nil {
		t.Fatal("older tree branch inherited accepted native binding")
	}
}
func TestReplySessionRestartLostAckAndGenerationFence(t *testing.T) {
	w := newWorld(t, "")
	r, call := nativeReceiverFixture(t, w.alice, "omp")
	binding, input := nativeInput(t, w.alice, w.bob, r.Handle)
	d, e := w.alice.TakeReplyReceiverInput(call)
	if e != nil {
		t.Fatal(e)
	}
	marker := map[string]any{"id": "marker", "type": "custom", "customType": "agentnet-receiver-session", "details": map[string]string{"handle": r.Handle}}
	nativeWrite(t, call.File, call.SessionID, marker, nativeEntry(d, "delivered", "marker"))
	// Crash before ACK: another process/generation must reconcile original token, never dispatch again.
	reopened, e := Open(w.alice.home)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	resumed, e := reopened.RegisterReplySession(ReplySessionRegistration{Harness: "omp", SessionID: call.SessionID, File: call.File, Leaf: "delivered", Handle: r.Handle})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.alice.TakeReplyReceiverInput(call); e == nil {
		t.Fatal("stale owner took input")
	}
	call.Generation, call.OwnerToken, call.Leaf = resumed.Generation, resumed.OwnerToken, "delivered"
	next, e := reopened.TakeReplyReceiverInput(call)
	if e != nil || next == nil || !next.ReconcileOnly || next.InputToken != d.InputToken {
		t.Fatalf("lost ACK %+v %v", next, e)
	}
	if ok, e := reopened.AckReplyReceiverInput(ReplyReceiverAck{ReplySessionCall: call, BindingID: binding, InputID: input, ClaimID: d.ClaimID, InputToken: d.InputToken}); e != nil || !ok {
		t.Fatalf("reconcile %v %v", ok, e)
	}
	public, e := reopened.ReplySessions()
	raw, _ := json.Marshal(public)
	if e != nil || strings.Contains(string(raw), resumed.OwnerToken) || strings.Contains(string(raw), call.File) {
		t.Fatal("owner secret/path public")
	}
}
func TestReplySessionInactiveUnknownRealmBranchAndCancel(t *testing.T) {
	w := newWorld(t, "")
	r, call := nativeReceiverFixture(t, w.alice, "pi")
	unknown := ReplyReceiver{Kind: "live_session", SessionHandle: "not registered"}
	if _, e := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "x", ReplyReceiver: &unknown}); e == nil {
		t.Fatal("unknown handle bound")
	}
	if _, e := w.alice.CurrentReplySession(r.Handle, w.bob.home, r.Generation); e == nil {
		t.Fatal("cross home inheritance")
	}
	if _, e := w.alice.RegisterReplySession(ReplySessionRegistration{Harness: "omp", SessionID: call.SessionID, File: call.File, Leaf: "marker", Handle: r.Handle}); e == nil {
		t.Fatal("different harness resume")
	}
	if _, e := w.alice.RegisterReplySession(ReplySessionRegistration{Harness: "pi", SessionID: call.SessionID, File: call.File, Leaf: "", Handle: r.Handle}); e == nil {
		t.Fatal("different branch resumed")
	}
	if e := w.alice.CloseReplySession(call); e != nil {
		t.Fatal(e)
	}
	binding, _ := nativeInput(t, w.alice, w.bob, r.Handle)
	rows := receiverBindings(t, w.alice)
	if rows[0].State != "pending" || !strings.Contains(rows[0].Detail, "inactive") {
		t.Fatalf("inactive route %+v", rows)
	}
	if _, e := w.alice.TakeReplyReceiverInput(call); e == nil {
		t.Fatal("inactive owner took")
	}
	if _, e := w.alice.store.db.Exec(`UPDATE reply_receivers SET canceled_at=1 WHERE id=?`, binding); e != nil {
		t.Fatal(e)
	}
	if e := w.alice.Approve(w.bob.Address); e != nil {
		t.Fatal(e)
	}
	if job, ok, e := w.alice.store.claimJob("stub"); e != nil || ok {
		t.Fatalf("canceled stolen %+v %v", job, e)
	}
	if _, e := w.alice.store.db.Exec(`UPDATE config SET v='foreign' WHERE k='realm_id'`); e != nil {
		t.Fatal(e)
	}
	if _, e := w.alice.RegisterReplySession(ReplySessionRegistration{Harness: "pi", SessionID: call.SessionID, File: call.File, Leaf: "marker", Handle: r.Handle}); e == nil {
		t.Fatal("cross realm resumed")
	}
}

func TestReplySessionLazyNativeAnchorAndChangedKey(t *testing.T) {
	w := newWorld(t, "")
	file := filepath.Join(t.TempDir(), "lazy.jsonl")
	r, e := w.alice.RegisterReplySession(ReplySessionRegistration{Harness: "omp", SessionID: "lazy-native", File: file, Leaf: "allocated-memory-leaf"})
	if e != nil {
		t.Fatal(e)
	}
	call := ReplySessionCall{Handle: r.Handle, Generation: r.Generation, OwnerToken: r.OwnerToken, SessionID: "lazy-native", File: file, Leaf: "allocated-memory-leaf"}
	binding, input := nativeInput(t, w.alice, w.bob, r.Handle)
	redirect := ReplyReceiver{Kind: "live_session", SessionHandle: "other", BindingID: binding}
	if _, e = w.alice.prepareReplyReceiver(&redirect); e == nil {
		t.Fatal("explicit binding redirect ignored")
	}
	// No physical JSONL until first native model turn: its allocated in-memory
	// leaf must not become an impossible durable take precondition.
	d, e := w.alice.TakeReplyReceiverInput(call)
	if e != nil || d == nil || d.ReconcileOnly {
		t.Fatalf("lazy first wake %+v %v", d, e)
	}
	marker := map[string]any{"type": "custom", "id": "marker", "data": map[string]string{"handle": r.Handle}, "customType": "agentnet-receiver-session"}
	nativeWrite(t, file, "lazy-native", marker, nativeEntry(d, "accepted", "marker"))
	bytes, e := os.ReadFile(file)
	if e != nil {
		t.Fatal(e)
	}
	bytes = append([]byte("{\"type\":\"title\",\"title\":\"native\"}\n"), bytes...)
	if e = os.WriteFile(file, bytes, 0600); e != nil {
		t.Fatal(e)
	}
	call.Leaf = "accepted"
	if _, e = w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.bob.Address); e != nil {
		t.Fatal(e)
	}
	ack := ReplyReceiverAck{ReplySessionCall: call, BindingID: binding, InputID: input, ClaimID: d.ClaimID, InputToken: d.InputToken}
	if _, e = w.alice.AckReplyReceiverInput(ack); e == nil {
		t.Fatal("changed key ACK")
	}
	if _, e = w.alice.TakeReplyReceiverInput(call); e == nil {
		t.Fatal("changed key take")
	}
	if _, ok, e := w.alice.store.claimJob("stub"); e != nil || ok {
		t.Fatalf("refused key default takeover %v %v", ok, e)
	}
	if _, e = w.alice.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.bob.Address); e != nil {
		t.Fatal(e)
	}
	if ok, e := w.alice.AckReplyReceiverInput(ack); e != nil || !ok {
		t.Fatalf("OMP title prefix/native custom data %v %v", ok, e)
	}
}
