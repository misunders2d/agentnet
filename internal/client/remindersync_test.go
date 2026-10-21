package client

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"slices"
	"testing"
	"time"
)

// A personal reminder follows the same current linked human devices as its original.
func TestReminderSyncOwnHumanJourney(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	const body = "F2 disposable reminder sync probe"
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: body}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "original on both own devices", func() bool {
		return len(convBodies(t, w.alice, conv)) == 1 && len(convBodies(t, phone, conv)) == 1
	})
	id, _ := readState(t, w.alice, conv, body)
	if _, err := w.alice.SetReminder(id, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.MarkRead([]string{id}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ordinary exact own-human read sync completes", func() bool {
		_, read := readState(t, phone, conv, body)
		return read
	})
	eventually(t, "reminder follows current own human", func() bool { rows, err := phone.Reminders(false); return err == nil && len(rows) == 1 })
	laptop, err := w.alice.Reminders(false)
	if err != nil {
		t.Fatal(err)
	}
	linkedPhone, err := phone.Reminders(false)
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := w.bob.Reminders(false)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("laptop=%d phone=%d other=%d", len(laptop), len(linkedPhone), len(foreign))
	phoneID := linkedPhone[0].Message
	due := time.Now().Add(2 * time.Hour)
	if _, err := phone.SetReminder(phoneID, due); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone edit reaches laptop", func() bool { r, ok, err := w.alice.Reminder(id); return err == nil && ok && r.Due == due.Unix() })
	if err := phone.DoneReminder(phoneID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "done reaches laptop", func() bool { r, ok, err := w.alice.Reminder(id); return err == nil && ok && r.State == ReminderDone })
	if _, err := w.alice.SetReminder(id, due); err != nil {
		t.Fatal(err)
	}
	eventually(t, "explicit recreate reaches phone", func() bool {
		r, ok, err := phone.Reminder(phoneID)
		return err == nil && ok && r.State == ReminderPending
	})
	if err := w.alice.CancelReminder(id); err != nil {
		t.Fatal(err)
	}
	eventually(t, "cancel reaches phone", func() bool {
		r, ok, err := phone.Reminder(phoneID)
		return err == nil && ok && r.State == ReminderCancelled
	})
	if _, err := w.alice.SetReminder(id, due); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reminder recreated for reply", func() bool {
		r, ok, err := phone.Reminder(phoneID)
		return err == nil && ok && r.State == ReminderPending
	})
	var logical string
	if err := phone.store.db.QueryRow(`SELECT lid FROM inbox WHERE id=?`, phoneID).Scan(&logical); err != nil {
		t.Fatal(err)
	}
	if _, err := phone.SendConv(tctx(t), conv, ConvOutgoing{Body: "explicit reply closes reminder", ReplyTo: logical}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reply completion syncs", func() bool { r, ok, err := w.alice.Reminder(id); return err == nil && ok && r.State == ReminderReplied })

	if len(laptop) != 1 || len(linkedPhone) != 1 || len(foreign) != 0 {
		t.Fatalf("reminder did not follow own human: laptop=%d phone=%d other=%d", len(laptop), len(linkedPhone), len(foreign))
	}
}

func TestReminderSyncMergeAuthorityAndLateOriginal(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.bob, w.alice)
	_, root := rootOf(t, w.bob, conv)
	stop()
	me, _, err := w.alice.store.selfPerson(w.alice.Address)
	if err != nil {
		t.Fatal(err)
	}
	original := protocol.NewID()
	base := protocol.ReminderMark{Conv: conv, Fingerprint: w.bob.Self().Fingerprint(), LID: original, Due: time.Now().Add(time.Hour).Unix(), State: ReminderPending, Revision: time.Now().UnixMilli(), Writer: phone.Self().Fingerprint()}
	receive := func(sender *Agent, m protocol.ReminderMark) string {
		t.Helper()
		raw, _ := json.Marshal(protocol.ReminderSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Reminders: []protocol.ReminderMark{m}})
		env := craft(t, sender, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubReminderSync, Replica: true, Body: string(raw)})
		if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
			t.Fatal(err)
		}
		return env.ID
	}
	receive(phone, base)
	if rows, err := w.alice.Reminders(false); err != nil || len(rows) != 0 {
		t.Fatalf("missing original fabricated a reminder: %+v %v", rows, err)
	}
	incoming := craft(t, w.bob, w.alice, envelope.Inner{Conv: conv, Root: root, LID: original, Kind: envelope.KindMessage, Body: "arrives after personal metadata"})
	if err := w.alice.verifyAndStore(tctx(t), incoming); err != nil {
		t.Fatal(err)
	}
	if rows, err := w.alice.Reminders(false); err != nil || len(rows) != 1 {
		t.Fatalf("late original: %+v %v", rows, err)
	}
	ended := base
	ended.Revision++
	ended.State = ReminderDone
	receive(phone, ended)
	receive(phone, base)
	if rows, _ := w.alice.Reminders(false); len(rows) != 0 {
		t.Fatal("stale pending resurrected a tombstone")
	}
	newer := ended
	newer.Revision++
	newer.State = ReminderPending
	if id := receive(w.bob, newer); heldReason(t, w.alice, id) == "" {
		t.Fatal("foreign person reminder accepted")
	}
	if n := count(t, w.alice, "inbox WHERE sub='reminder-sync'"); n != 0 {
		t.Fatal("sync carrier became a timeline turn")
	}
	conflicting := ended
	conflicting.Due++
	if id := receive(phone, conflicting); heldReason(t, w.alice, id) == "" {
		t.Fatal("conflicting signed generation was not quarantined")
	}
	// Clock order is deterministic; same-generation equivocation is refused.
	tx, err := w.alice.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	conflict := ended
	conflict.Due++
	if _, err := saveReminderMark(tx, me.info.Person, conflict); err == nil {
		t.Fatal("conflicting generation accepted")
	}
	tx.Rollback()
	// The same authority used at admission is checked again at delivery.
	raw, _ := json.Marshal(me.roster)
	me.roster.HumanKeys = []string{w.alice.Self().Fingerprint()}
	changed, _ := json.Marshal(me.roster)
	if _, err = w.alice.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(changed), personSelf); err != nil {
		t.Fatal(err)
	}
	if id := receive(phone, newer); heldReason(t, w.alice, id) == "" {
		t.Fatal("agent-host sender accepted")
	}
	w.alice.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(raw), personSelf)
	if _, err = w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if id := receive(phone, newer); heldReason(t, w.alice, id) == "" {
		t.Fatal("pending sender key accepted")
	}
}

func TestReminderSyncOldReaderRestartAndDelivery(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone, awaited, _ := linkPhone(t, w.alice, "phone")
	req := pendingLink(t, w.alice)
	if err := w.alice.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	n := fakeNotify(phone)
	runAgent(t, phone)
	sent, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "own-sent reminder original"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "own-sent original replicated", func() bool { _, err := phone.Conversation(sent.ID, 0, 0); return err == nil })
	stop()
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "phone publishes reader", func() bool {
		return w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile) == nil && len(profile.Sessions) > 0
	})
	publish := func(ts int64, caps []string) {
		for _, session := range profile.Sessions {
			r := protocol.CapsRecord{Address: phone.Address, Session: session, TS: ts, Caps: caps}
			r.Sign(phone.id.Sign)
			if err := phone.hub.do(tctx(t), "PUT", "/v1/caps", r, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	publish(time.Now().Unix()+100, slices.DeleteFunc(slices.Clone(ownCaps), func(c string) bool { return c == protocol.CapReminderSync }))
	due := time.Now().Add(time.Hour)
	if _, err = w.alice.SetReminder(sent.ID, due); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.syncReminders(); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='reminder-sync'`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	json.Unmarshal([]byte(encoded), &env)
	if r, err := w.alice.deliver(tctx(t), env, nil); err != nil || r.State != stateConvWaiting {
		t.Fatalf("old reader: %+v %v", r, err)
	}
	if _, err = w.alice.SetReminder(sent.ID, due.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.syncReminders(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, w.alice, "outbox WHERE sub='reminder-sync'"); n != 1 {
		t.Fatalf("old reader accumulated %d carriers", n)
	}
	ordinary, err := w.alice.SendMessage(tctx(t), Outgoing{To: phone.Address, Kind: envelope.KindMessage, Body: "ordinary progresses past unsupported state"})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "ordinary reaches old reader", func() bool { return inboxCount(t, phone, `id=?`, ordinary.ID) == 1 })
	if rows, _ := phone.Reminders(false); len(rows) != 0 {
		t.Fatal("unsupported reader received reminder")
	}
	if err = w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err = again.syncReminders(); err != nil {
		t.Fatal(err)
	}
	if n := count(t, again, "outbox WHERE sub='reminder-sync'"); n != 1 {
		t.Fatalf("restart duplicated carrier: %d", n)
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
	if _, err = again.syncReminders(); err != nil {
		t.Fatal(err)
	}
	if err = again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "latest reminder catches up after update", func() bool {
		rows, err := phone.Reminders(false)
		return err == nil && len(rows) == 1 && rows[0].Due == due.Add(time.Hour).Unix()
	})
	before := n.count()
	if _, err = phone.remindDue(due.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if n.count() != before+1 {
		t.Fatalf("due notifications delta: %d", n.count()-before)
	}
	before = n.count()
	if _, err = phone.remindDue(due.Add(3 * time.Hour)); err != nil || n.count() != before {
		t.Fatalf("same generation re-alerted: %d %v", n.count(), err)
	}
	// A second handle models reopening persisted delivery state, without another daemon.
	reopened, err := Open(phone.home)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	n2 := fakeNotify(reopened)
	if _, err = reopened.remindDue(due.Add(3 * time.Hour)); err != nil || n2.count() != 0 {
		t.Fatalf("restart re-alerted: %d %v", n2.count(), err)
	}
}
