package client

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"slices"
	"testing"
	"time"
)

func readState(t *testing.T, a *Agent, conv, body string) (string, bool) {
	t.Helper()
	var id string
	var read bool
	if err := a.store.db.QueryRow(`SELECT id,read_at IS NOT NULL FROM inbox WHERE conv=? AND body=?`, conv, body).Scan(&id, &read); err != nil {
		t.Fatal(err)
	}
	return id, read
}
func TestReadSyncExactOwnHumanJourney(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	for _, body := range []string{"old read turn", "new unseen turn"} {
		if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "two originals on both own devices", func() bool { return len(convBodies(t, w.alice, conv)) == 2 && len(convBodies(t, phone, conv)) == 2 })
	old, _ := readState(t, w.alice, conv, "old read turn")
	if err := w.alice.MarkRead([]string{old}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "exact read converges", func() bool { _, read := readState(t, phone, conv, "old read turn"); return read })
	if _, read := readState(t, phone, conv, "new unseen turn"); read {
		t.Fatal("newer unseen turn cleared")
	}
	// A verified own-human marker arriving before its original persists safely.
	me, _, err := phone.store.selfPerson(phone.Address)
	if err != nil {
		t.Fatal(err)
	}
	future := protocol.NewID()
	r := protocol.ReadSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Refs: []protocol.ReadRef{{Conv: conv, Fingerprint: w.bob.Self().Fingerprint(), LID: future}}}
	raw, _ := json.Marshal(r)
	carrier := craft(t, phone, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if err = w.alice.verifyAndStore(tctx(t), carrier); err != nil {
		t.Fatal(err)
	}
	_, root := rootOf(t, w.alice, conv)
	arrival := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: future, Body: "marked before arrival"})
	if err = w.alice.verifyAndStore(tctx(t), arrival); err != nil {
		t.Fatal(err)
	}
	if _, read := readState(t, w.alice, conv, "marked before arrival"); !read {
		t.Fatal("future exact arrival missed durable mark")
	}
	foreign := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReadSync, Replica: true, Body: string(raw)})
	if err = w.alice.verifyAndStore(tctx(t), foreign); err != nil {
		t.Fatal(err)
	}
	if heldReason(t, w.alice, foreign.ID) == "" {
		t.Fatal("another person's read state accepted")
	}
	// Current human authority, rather than historical roster inclusion, decides.
	if err = readSyncAuthority(w.alice.store.db, r, w.bob.Address, w.bob.Self().Fingerprint(), w.alice.Address, w.alice.Self().Fingerprint()); err == nil {
		t.Fatal("foreign sender authority")
	}
	original, _ := json.Marshal(me.roster)
	me.roster.HumanKeys = []string{phone.Self().Fingerprint()}
	record, _ := json.Marshal(me.roster)
	if _, err = phone.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(record), personSelf); err != nil {
		t.Fatal(err)
	}
	if err = readSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("agent-host device supplied read state")
	}
	if _, err = phone.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(original), personSelf); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if err = readSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("pending peer key accepted")
	}
	if _, err = phone.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if _, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE state=?`, personConflict, personSelf); err != nil {
		t.Fatal(err)
	}
	if err = readSyncAuthority(phone.store.db, r, w.alice.Address, w.alice.Self().Fingerprint(), phone.Address, phone.Self().Fingerprint()); err == nil {
		t.Fatal("frozen self person accepted")
	}
	if _, err = phone.store.db.Exec(`UPDATE persons SET state=? WHERE person=?`, personSelf, r.Person); err != nil {
		t.Fatal(err)
	}
	if len(ownCaps)+1 > protocol.MaxAdvertisedCaps {
		t.Fatal("cap bound exceeded")
	}
}
func TestReadSyncOlderReaderRestartAndKeyFence(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	conv := newDM(t, w.alice, w.bob)
	_, root := rootOf(t, w.alice, conv)
	item := craft(t, w.bob, w.alice, envelope.Inner{Kind: envelope.KindMessage, Conv: conv, Root: root, LID: protocol.NewID(), Body: "restart mark"})
	if err := w.alice.verifyAndStore(tctx(t), item); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.MarkRead([]string{item.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub=? AND recipient=? LIMIT 1`, envelope.SubReadSync, phone.Address).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	if err := json.Unmarshal([]byte(encoded), &env); err != nil {
		t.Fatal(err)
	}
	if env.Attn {
		t.Fatal("read marker requests notification")
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
		t.Fatalf("legacy reader %+v %v", result, err)
	}
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err = again.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, again, "outbox WHERE sub='read-sync'"); n != 1 {
		t.Fatalf("restart duplicated mark: %d", n)
	}
	publish(time.Now().Unix()+200, slices.Clone(ownCaps))
	features, err := again.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	again.releaseConv(tctx(t), features)
	if err = again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	var unchanged string
	if err = again.store.db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, env.ID).Scan(&unchanged); err != nil || unchanged != encoded {
		t.Fatal("restoration changed captured envelope", err)
	}
	if err = again.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = again.store.setOutboxState(env.ID, stateQueued, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := again.mayDeliverReadSync(env); err != nil || ok {
		t.Fatalf("removed reader admitted: %v %v", ok, err)
	}
}

func TestReadSyncBoundedBatchRecovery(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	me, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < protocol.MaxReadRefs+1; i++ {
		if _, err = w.alice.store.db.Exec(`INSERT INTO read_marks(owner,conv,fingerprint,lid) VALUES(?,'',?,?)`, me.info.Person, w.bob.Self().Fingerprint(), protocol.NewID()); err != nil {
			t.Fatal(err)
		}
	}
	more, err := w.alice.syncReadMarks()
	if err != nil || !more {
		t.Fatalf("first page: %v %v", more, err)
	}
	more, err = w.alice.syncReadMarks()
	if err != nil || more {
		t.Fatalf("second page: %v %v", more, err)
	}
	if _, err = w.alice.syncReadMarks(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, w.alice, "outbox WHERE sub='read-sync'"); n != 2 {
		t.Fatalf("65 references require two durable carriers, got %d", n)
	}
	rows, err := w.alice.store.db.Query(`SELECT body,recipient FROM outbox WHERE sub='read-sync'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	refs := 0
	for rows.Next() {
		var raw, to string
		if err = rows.Scan(&raw, &to); err != nil {
			t.Fatal(err)
		}
		r, e := protocol.ParseReadSync([]byte(raw))
		if e != nil || to != phone.Address {
			t.Fatal("invalid batch", e)
		}
		refs += len(r.Refs)
	}
	if refs != protocol.MaxReadRefs+1 {
		t.Fatalf("batch lost exact references %d", refs)
	}
}
