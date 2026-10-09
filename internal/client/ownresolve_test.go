package client

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An explicit owner decision closes only the exact host request/attempt. A
// later unrelated answer is never evidence that this earlier work completed.
func TestOwnHumanResolveExactRequest(t *testing.T) {
	w := newWorld(t, "")
	if _, e := w.alice.CreatePerson(tctx(t), "Alice"); e != nil {
		t.Fatal(e)
	}
	runWith(t, w, w.alice, RunOptions{})
	phone := linkedVia(t, w.alice, "phone", func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) })
	q, e := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "unresolved original task"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "original stored", func() bool { _, e := w.alice.store.jobState(q.ID); return e == nil })
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,detail='Needs an environment repair' WHERE id=?`, stateNeedHuman, q.ID); e != nil {
		t.Fatal(e)
	}
	send := func(sender *Agent, id, key string, attempt int64) (envelope.Envelope, envelope.Inner) {
		t.Helper()
		body, _ := json.Marshal(envelope.Decision{Action: "resolve", Expect: stateNeedHuman, Attempt: attempt})
		in := envelope.Inner{V: envelope.Version3, ID: protocol.NewID(), From: sender.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: envelope.SubDecision, Body: string(body), Ref: &envelope.Ref{ID: id, Fingerprint: key}}
		r, _ := w.alice.Self().Recipient()
		env, e := envelope.Seal(in, sender.id.Sign, r)
		if e != nil {
			t.Fatal(e)
		}
		if e = w.alice.admitDecision(tctx(t), env, in, sender.Self()); e != nil {
			t.Fatal(e)
		}
		return env, in
	}
	assertState := func(want string) {
		t.Helper()
		if state, _ := w.alice.store.jobState(q.ID); state != want {
			t.Fatalf("state %s; want %s", state, want)
		}
	}
	for _, tc := range []struct {
		sender  *Agent
		key     string
		attempt int64
	}{
		{w.bob, phone.Self().Fingerprint(), 1},
		{phone, w.bob.Self().Fingerprint(), 1},
		{phone, phone.Self().Fingerprint(), 2},
	} {
		send(tc.sender, q.ID, tc.key, tc.attempt)
		assertState(stateNeedHuman)
	}
	env, in := send(phone, q.ID, phone.Self().Fingerprint(), 1)
	assertState(stateResolved)
	if e = w.alice.admitDecision(tctx(t), env, in, phone.Self()); e != nil {
		t.Fatal(e)
	}
	assertState(stateResolved)
	var attempts, continuations int
	if e = w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, q.ID).Scan(&attempts); e != nil {
		t.Fatal(e)
	}
	if e = w.alice.store.db.QueryRow(`SELECT count(*) FROM request_continuations WHERE request=?`, q.ID).Scan(&continuations); e != nil {
		t.Fatal(e)
	}
	if attempts != 1 || continuations != 0 {
		t.Fatal("resolving queued another run", attempts, continuations)
	}
	// The same current person includes agent hosts, but only its human devices
	// own these decisions. Neither a host key nor a removed phone can resolve.
	agentHost := linkedVia(t, w.alice, "worker", w.alice.ApproveAgentLink)
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=2 WHERE id=?`, stateNeedHuman, q.ID); e != nil {
		t.Fatal(e)
	}
	send(agentHost, q.ID, phone.Self().Fingerprint(), 2)
	assertState(stateNeedHuman)
	if e = w.alice.RemoveDevice(tctx(t), phone.Address); e != nil {
		t.Fatal(e)
	}
	send(phone, q.ID, phone.Self().Fingerprint(), 2)
	assertState(stateNeedHuman)
}

func TestOwnHumanResolveSyncedRequest(t *testing.T) {
	w := newWorld(t, "")
	if _, e := w.alice.CreatePerson(tctx(t), "Alice"); e != nil {
		t.Fatal(e)
	}
	runWith(t, w, w.alice, RunOptions{})
	link := func(ctx context.Context, id string) error { return w.alice.DecideLink(ctx, id, true) }
	phone := linkedVia(t, w.alice, "phone", link)
	tablet := linkedVia(t, w.alice, "tablet", link)
	q, e := phone.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindQuestion, Body: "sibling device request"})
	if e != nil {
		t.Fatal(e)
	}
	eventually(t, "host stores original", func() bool { return inboxHas(t, w.alice, q.ID) })
	if _, e = w.alice.store.db.Exec(`UPDATE inbox SET state=?,attempts=1,detail='Needs an environment repair' WHERE id=?`, stateNeedHuman, q.ID); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if _, e = w.alice.deviceHistoryPage(tablet.Self()); e != nil {
			t.Fatal(e)
		}
	}
	deliverDeviceHistory(t, w.alice, tablet, envelope.SubDeviceHistory)
	exec := &ExecView{Host: w.alice.Address, State: stateNeedHuman, Attempt: 1}
	descriptor, e := tablet.ContinuationFor(q.ID, "", exec)
	if e != nil || descriptor == nil || descriptor.Host != w.alice.Address || descriptor.Key != phone.Self().Fingerprint() {
		t.Fatalf("synced descriptor: %+v %v", descriptor, e)
	}
	sendID := protocol.NewID()
	sent, e := tablet.Decide(WithQueuedSend(tctx(t), sendID), descriptor.Host, descriptor.ID, descriptor.Key, "resolve", stateNeedHuman, 1, "", "")
	if e != nil || sent.ID != sendID {
		t.Fatalf("queued resolution: %+v %v", sent, e)
	}
	waitState(t, w.alice, q.ID, stateResolved)
	var required string
	if e = tablet.store.db.QueryRow(`SELECT required_cap FROM outbox WHERE id=?`, sendID).Scan(&required); e != nil || required != protocol.CapOwnSyncV3 {
		t.Fatalf("decision compatibility: %s %v", required, e)
	}
	if _, e = tablet.Decide(WithQueuedSend(tctx(t), sendID), descriptor.Host, descriptor.ID, descriptor.Key, "resolve", stateNeedHuman, 1, "", ""); e != nil {
		t.Fatal(e)
	}
	var attempts int
	if e = w.alice.store.db.QueryRow(`SELECT attempts FROM inbox WHERE id=?`, q.ID).Scan(&attempts); e != nil || attempts != 1 {
		t.Fatalf("resolution reran the original: %d %v", attempts, e)
	}
}
