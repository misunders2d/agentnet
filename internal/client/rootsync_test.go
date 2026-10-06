package client

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func hasRoot(t *testing.T, a *Agent, conv string) bool {
	t.Helper()
	_, _, ok, err := a.store.conversation(conv)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestRootSyncEmptyBeforeAndAfterLink(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	before := newDM(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	eventually(t, "empty pre-link root on phone", func() bool { return hasRoot(t, phone, before) })
	_, aRaw := rootOf(t, w.alice, before)
	_, pRaw := rootOf(t, phone, before)
	if string(aRaw) != string(pRaw) {
		t.Fatal("signed root changed")
	}
	if len(convBodies(t, phone, before)) != 0 {
		t.Fatal("root sync fabricated a turn")
	}
	if unread, err := phone.ConvUnread(); err != nil || len(unread) != 0 {
		t.Fatalf("root unread: %+v %v", unread, err)
	}
	if inbox, err := phone.Inbox(false, false); err != nil || len(inbox) != 0 {
		t.Fatalf("root is visible in inbox: %+v %v", inbox, err)
	}
	if count(t, phone, "inbox WHERE state != ''") != 0 {
		t.Fatal("root created work")
	}
	after := newDM(t, phone, w.bob)
	eventually(t, "empty phone-created root on laptop", func() bool { return hasRoot(t, w.alice, after) })
	_, aRaw = rootOf(t, w.alice, after)
	_, pRaw = rootOf(t, phone, after)
	if string(aRaw) != string(pRaw) {
		t.Fatal("phone root changed")
	}
	if _, err := phone.SendConv(tctx(t), after, ConvOutgoing{Body: "first real turn"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "real turn uses synced root on laptop and peer", func() bool { return len(convBodies(t, w.alice, after)) == 1 && len(convBodies(t, w.bob, after)) == 1 })
	var first string
	phone.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND conv=? LIMIT 1`, envelope.SubRootSync, after).Scan(&first)
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(first), &env); err != nil {
		t.Fatal(err)
	}
	if env.Attn {
		t.Fatal("root requested notification")
	}
	if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if len(convBodies(t, w.alice, after)) != 1 {
		t.Fatal("root replay fabricated a duplicate turn")
	}
}

func TestRootSyncRecoveryAuthorityAndOlderReader(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	conv := newDM(t, w.alice, w.bob)
	root, raw := rootOf(t, w.alice, conv)
	if _, err := w.alice.syncRoots(); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	var encoded string
	w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND sub=? AND recipient=? LIMIT 1`, conv, envelope.SubRootSync, phone.Address).Scan(&encoded)
	if err := json.Unmarshal([]byte(encoded), &env); err != nil {
		t.Fatal(err)
	}
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	if err := w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil {
		t.Fatal(err)
	}
	publish := func(ts int64, caps []string) {
		rec := protocol.CapsRecord{Address: phone.Address, Session: profile.Sessions[0], TS: ts, Caps: caps}
		rec.Sign(phone.id.Sign)
		if err := phone.hub.do(tctx(t), "PUT", "/v1/caps", rec, nil); err != nil {
			t.Fatal(err)
		}
	}
	publish(time.Now().Unix()+100, []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapRoom})
	if result, err := w.alice.deliver(tctx(t), env, nil); err != nil || result.State != stateConvWaiting {
		t.Fatalf("old-reader gate %+v %v", result, err)
	}
	if hasRoot(t, phone, conv) {
		t.Fatal("old reader received unknown root subtype")
	}
	if _, err := w.alice.syncRoots(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, w.alice, "outbox WHERE sub='root-sync' AND conv='"+conv+"'"); n != 1 {
		t.Fatalf("waiting reconciliation duplicated carrier: %d", n)
	}
	// Reopen the sender: persisted root/outbox recover without restarting history.
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	caps := slices.Clone(ownCaps)
	publish(time.Now().Unix()+200, caps)
	feats, err := again.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	again.releaseConv(tctx(t), feats)
	if err := again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "persisted root delivered after reader update", func() bool { return hasRoot(t, phone, conv) })
	if _, err := again.rootSyncCopy(root, raw, w.bob.Self()); err == nil {
		t.Fatal("root exported to foreign device")
	}
	bad := craft(t, w.bob, phone, envelope.Inner{Conv: conv, Root: raw, LID: protocol.NewID(), Kind: envelope.KindMessage, Sub: envelope.SubRootSync, Replica: true, Body: `{"v":1}`})
	if err := phone.verifyAndStore(tctx(t), bad); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, phone, bad.ID) == "" {
		t.Fatal("foreign sender was admitted")
	}
	// Exact current-human check is independent of the historical creator key.
	me, _, err := again.store.selfPerson(again.Address)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(me.roster)
	me.roster.HumanKeys = []string{again.Self().Fingerprint()}
	record, _ := json.Marshal(me.roster)
	if _, err := again.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(record), personSelf); err != nil {
		t.Fatal(err)
	}
	if err := rootSyncAuthority(again.store.db, root, again.Address, again.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("agent-host recipient accepted")
	}
	if err := rootSyncAuthority(again.store.db, root, phone.Address, phone.Self().Fingerprint(), again.Address, again.Self().Fingerprint()); err == nil {
		t.Fatal("agent-host sender accepted")
	}
	if _, err := again.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(original), personSelf); err != nil {
		t.Fatal(err)
	}
	freeze(t, again, w.bob)
	if _, err := again.rootSyncCopy(root, raw, phone.Self()); !errors.Is(err, errPersonConflict) {
		t.Fatalf("frozen member not refused as conflict: %v", err)
	}
	if len(ownCaps)+1 > protocol.MaxAdvertisedCaps {
		t.Fatal("agent hint exceeds cap bound")
	}
}

func TestRootSyncRemovedAndCapturedKeyFailClosed(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	conv := newDM(t, w.alice, w.bob)
	root, raw := rootOf(t, w.alice, conv)
	if _, err := w.alice.syncRoots(); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE conv=? AND sub=?`, conv, envelope.SubRootSync).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(encoded), &env); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.sendKey(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if ok, err := w.alice.rootSyncDelivery(tctx(t), env, w.bob.Self().Fingerprint()); err != nil || ok {
		t.Fatalf("different saved recipient key permitted: %v %v", ok, err)
	}
	if err := w.alice.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.rootSyncCopy(root, raw, phone.Self()); err == nil {
		t.Fatal("removed recipient permitted")
	}
	if err := rootSyncAuthority(w.alice.store.db, root, phone.Address, phone.Self().Fingerprint(), w.alice.Address, w.alice.Self().Fingerprint()); err == nil {
		t.Fatal("removed sender permitted")
	}
	if ok, err := w.alice.rootSyncDelivery(tctx(t), env, phone.Self().Fingerprint()); err != nil || ok {
		t.Fatalf("removed durable recipient permitted: %v %v", ok, err)
	}
}
