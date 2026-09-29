package client

import (
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// linked links a new device "phone" to a's person and runs its daemon.
func linked(t *testing.T, a *Agent) *Agent {
	t.Helper()
	phone, awaited, _ := linkPhone(t, a, "phone")
	req := pendingLink(t, a)
	if err := a.DecideLink(tctx(t), req.ID, true); err != nil {
		t.Fatal(err)
	}
	if out := <-awaited; out.err != nil {
		t.Fatal(out.err)
	}
	runAgent(t, phone)
	return phone
}

// T2 (history): a newly linked device gets the person's existing chats,
// as history: both directions, files as manifests, shown as synced; none of
// it runs or alerts, and a later message comes directly.
func TestHistoryToLinkedDevice(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	path, _ := writeFile(t, t.TempDir(), "notes.txt", 100)
	for _, m := range []struct {
		from *Agent
		out  ConvOutgoing
	}{
		{w.bob, ConvOutgoing{Body: "hello alice"}},
		{w.alice, ConvOutgoing{Body: "hi bob", Files: []OutgoingFile{{Path: path}}}},
		{w.bob, ConvOutgoing{Kind: envelope.KindQuestion, Body: "lunch?"}},
	} {
		if _, err := m.from.SendConv(tctx(t), conv, m.out); err != nil {
			t.Fatal(err)
		}
		eventually(t, "the laptop to have the conversation", func() bool { return len(convBodies(t, w.alice, conv)) > 0 })
	}
	eventually(t, "the laptop to hold all three", func() bool { return len(convBodies(t, w.alice, conv)) == 3 })
	phone := linked(t, w.alice)
	eventually(t, "the history on the phone", func() bool {
		return strings.Join(convBodies(t, phone, conv), "|") == "in:hello alice|out:hi bob|in:lunch?"
	})
	msgs, _ := phone.ConversationMessages(conv)
	for _, m := range msgs {
		if !m.History || m.SyncedFrom != w.alice.Address || m.Key != "" || m.Job != "" || m.State != "" {
			t.Fatalf("history message %+v", m)
		}
	}
	if len(msgs[1].Attachments) != 1 || msgs[1].Attachments[0].Name != "notes.txt" || msgs[1].Via != w.alice.Address {
		t.Fatalf("the file message: %+v", msgs[1])
	}
	eventually(t, "the snapshot done", func() bool {
		jobs, _ := w.alice.HistoryProgress()
		return len(jobs) == 1 && jobs[0].State == "done" && jobs[0].ConvsDone == 1 && jobs[0].Name == "phone"
	})
	sentBoth, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "now to both"})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("sent %+v", sentBoth.Copies)
	deadline := time.Now().Add(15 * time.Second)
	for {
		msgs, _ := phone.ConversationMessages(conv)
		if len(msgs) == 4 && !msgs[3].History && msgs[3].Key == w.bob.Self().Fingerprint() {
			break
		}
		if time.Now().After(deadline) {
			for _, m := range msgs {
				t.Logf("%s %s hist=%v key=%s", m.Dir, m.Body, m.History, m.Key)
			}
			t.Fatal("no direct message on the phone")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A message sent to an older roster of the person (the sender did not know
// the new device yet) is forwarded by the device that got it; a copy that
// arrives directly later takes its place.
func TestHistoryForwardsStaleFan(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	if _, err := w.bob.SendConv(tctx(t), conv, ConvOutgoing{Body: "first"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first message", func() bool { return len(convBodies(t, w.alice, conv)) == 1 })
	old, _, _ := w.alice.Person()
	phone := linked(t, w.alice)
	eventually(t, "the first message on the phone", func() bool { return len(convBodies(t, phone, conv)) == 1 })
	bobMe, _, _ := w.bob.Person()
	_, raw := rootOf(t, w.alice, conv)
	stale := envelope.Inner{Kind: envelope.KindMessage, Body: "sent to the old roster", Conv: conv, LID: protocol.NewID(), Root: raw,
		Origin: envelope.OriginUI, Fan: []envelope.Fan{{Person: bobMe.Person, Roster: bobMe.Roster}, {Person: old.Person, Roster: old.Roster}}}
	env := craft(t, w.bob, w.alice, stale)
	if err := w.alice.verifyAndStore(tctx(t), env); err != nil {
		t.Fatal(err)
	}
	if n := count(t, w.alice, "outbox WHERE sub = 'history' AND recipient = '"+phone.Address+"'"); n < 2 {
		var errs []string
		rows, _ := w.alice.store.db.Query(`SELECT id, state, coalesce(error,'') FROM outbox WHERE sub = 'history'`)
		for rows.Next() {
			var a, b, c string
			rows.Scan(&a, &b, &c)
			errs = append(errs, a+" "+b+" "+c)
		}
		rows.Close()
		t.Fatalf("history copies at the laptop: %d %v", n, errs)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		msgs, _ := phone.ConversationMessages(conv)
		if len(msgs) == 2 && msgs[1].Body == "sent to the old roster" && msgs[1].History {
			break
		}
		if time.Now().After(deadline) {
			var info []string
			rows, _ := w.alice.store.db.Query(`SELECT id, state, coalesce(error,'') FROM outbox WHERE sub = 'history'`)
			for rows.Next() {
				var a, b, c string
				rows.Scan(&a, &b, &c)
				info = append(info, "out "+a+" "+b+" "+c)
			}
			rows.Close()
			rows, _ = phone.store.db.Query(`SELECT id, reason FROM quarantine`)
			for rows.Next() {
				var a, b string
				rows.Scan(&a, &b)
				info = append(info, "held "+a+" "+b)
			}
			rows.Close()
			for _, m := range msgs {
				info = append(info, "msg "+m.Body)
			}
			t.Fatalf("no forwarded copy: %v", info)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// The same message directly, later: it takes the history copy's place.
	direct := stale
	direct.ID = protocol.NewID()
	if err := phone.verifyAndStore(tctx(t), craft(t, w.bob, phone, direct)); err != nil {
		t.Fatal(err)
	}
	msgs, _ := phone.ConversationMessages(conv)
	if len(msgs) != 2 || msgs[1].History || msgs[1].Key != w.bob.Self().Fingerprint() {
		t.Fatalf("after the direct copy: %+v", msgs)
	}
	if n := inboxCount(t, phone, `lid = ?`, stale.LID); n != 1 {
		t.Fatalf("%d rows for one message", n)
	}
}

// A request to this device's agent received directly creates its job
// whether a history copy of it came first or comes later; a history copy
// alone never does.
func TestHistoryNeverSwallowsDirectRequest(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	persons(t, w.alice, w.bob)
	conv := newDM(t, w.bob, w.alice)
	_, raw := rootOf(t, w.bob, conv)
	for _, historyFirst := range []bool{true, false} {
		in := envelope.Inner{V: envelope.Version2, ID: protocol.NewID(), From: w.bob.Address, TS: time.Now().Unix(), Kind: envelope.KindQuestion,
			Body: "run this", Conv: conv, LID: protocol.NewID(), Root: raw, PID: protocol.NewID(),
			Target: &envelope.Target{Address: w.alice.Address, Fingerprint: w.alice.Self().Fingerprint()}}
		key := w.bob.Self().Fingerprint()
		hist := func() {
			h := itemOf(in, key).inner(conv)
			if _, err := w.alice.store.addHistoryInbox(h, key, "admin/laptop", protocol.NewID(), false, nil); err != nil {
				t.Fatal(err)
			}
		}
		if historyFirst {
			hist()
			if n := inboxCount(t, w.alice, `lid = ? AND state = ?`, in.LID, stateAgentWaiting); n != 0 {
				t.Fatal("history made a job")
			}
		}
		if _, err := w.alice.store.addConvInbox(in, key, stateAgentWaiting, false, nil); err != nil {
			t.Fatal(err)
		}
		if !historyFirst {
			hist()
		}
		if n := inboxCount(t, w.alice, `lid = ?`, in.LID); n != 1 {
			t.Fatalf("history first %v: %d rows", historyFirst, n)
		}
		if n := inboxCount(t, w.alice, `lid = ? AND state = ? AND verified_by = ? AND claimed_fp IS NULL`, in.LID, stateAgentWaiting, key); n != 1 {
			t.Fatalf("history first %v: the direct request has no job", historyFirst)
		}
	}
}
