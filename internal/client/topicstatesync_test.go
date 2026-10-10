package client

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func markAt(t *testing.T, a *Agent, scope, topic string) topicLocal {
	t.Helper()
	l, err := a.store.topicLocals(scope)
	if err != nil {
		t.Fatal(err)
	}
	return l[topic]
}

// Owner (v0.8.17): "why are names synced, but statuses aren't?" Mark done,
// Reopen and Archive follow the person's own human devices as names do, in
// device chats and in people chats, and never reach another person.
func TestTopicMarkSyncOwnHumanJourney(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	msg, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "Device topic"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "device topic reaches own phone", func() bool {
		ts, e := phone.TopicOf(msg.ID)
		return e == nil && ts.State == TopicActive
	})
	if covered, err := w.alice.MarkTopicDone(w.bob.Address, msg.ID, 0); err != nil || !covered {
		t.Fatalf("mark done covered=%v err=%v", covered, err)
	}
	eventually(t, "laptop Mark done is done on the phone", func() bool {
		ts, e := phone.TopicOf(msg.ID)
		l := markAt(t, phone, w.bob.Address, msg.ID)
		return e == nil && ts.State == TopicDone && ts.DoneBy == DoneByYou && l.MarkCount == 1
	})
	if _, err = phone.ReopenTopic(w.bob.Address, msg.ID, 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "phone Reopen reaches the laptop", func() bool {
		ts, e := w.alice.TopicOf(msg.ID)
		return e == nil && ts.State == TopicActive && markAt(t, w.alice, w.bob.Address, msg.ID).Mark == topicMarkOpen
	})
	if err = w.alice.ArchiveTopic(w.bob.Address, msg.ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, "laptop Archive is archived on the phone", func() bool {
		ts, e := phone.TopicOf(msg.ID)
		return e == nil && ts.State == TopicArchived
	})

	conv := newDM(t, w.alice, w.bob)
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Topic in a DM", Topic: "new"}); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Main flow note"}); err != nil {
		t.Fatal(err)
	}
	var topic string
	eventually(t, "DM topic and Main flow reach own phone", func() bool {
		ts, e := phone.ChatTopics(conv)
		if e != nil || len(ts) != 1 {
			return false
		}
		topic = ts[0].ID
		msgs, e := phone.ConversationMessages(conv)
		return e == nil && len(mainChatMessages(msgs)) == 1
	})
	if _, err = w.alice.ChangeChatTopic(tctx(t), conv, topic, "archive", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.ChangeChatMainTopic(tctx(t), conv, "archive", "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "DM topic and Main flow archive reach own phone", func() bool {
		ts, e := phone.ChatTopics(conv)
		if e != nil || len(ts) != 1 || ts[0].State != TopicArchived {
			return false
		}
		msgs, e := phone.ConversationMessages(conv)
		if e != nil {
			return false
		}
		main, e := phone.ChatMainTopicForMessages(conv, msgs)
		return e == nil && main != nil && main.State == TopicArchived
	})
	for _, a := range []*Agent{w.alice, phone, w.bob} {
		if n := count(t, a, "inbox WHERE sub='topic-state-sync'"); n != 0 {
			t.Fatalf("%s: %d private marks became messages", a.Address, n)
		}
	}
	if n := count(t, w.bob, "topic_state WHERE coalesce(mark,'')!=''"); n != 0 {
		t.Fatalf("another person received %d private marks", n)
	}
	ts, err := w.bob.ChatTopics(conv)
	if err != nil || len(ts) != 1 || ts[0].State == TopicArchived {
		t.Fatalf("private archive changed another person's topic: %+v %v", ts, err)
	}
}

func TestTopicMarkSyncConvergesAndFailsClosed(t *testing.T) {
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
	conv, topic, now := strings.Repeat("a", 64), protocol.NewID(), time.Now().Unix()
	first := protocol.TopicMark{Scope: conv, Topic: topic, Mark: protocol.TopicMarkArchived, Count: 4, At: now + 100, Writer: phone.Self().Fingerprint()}
	receive := func(from *Agent, marks ...protocol.TopicMark) string {
		t.Helper()
		raw, _ := json.Marshal(protocol.TopicStateSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Marks: marks})
		env := craft(t, from, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubTopicStateSync, Replica: true, Body: string(raw)})
		if e := w.alice.verifyAndStore(tctx(t), env); e != nil {
			t.Fatal(e)
		}
		return env.ID
	}
	expect := func(what, mark string, count int, at int64) {
		t.Helper()
		if l := markAt(t, w.alice, conv, topic); l.Mark != mark || l.MarkCount != count || l.MarkAt != at {
			t.Fatalf("%s: %+v, want %s/%d/%d", what, l, mark, count, at)
		}
	}
	receive(phone, first)
	expect("own phone mark", protocol.TopicMarkArchived, 4, now+100)
	older := first
	older.Mark, older.At = protocol.TopicMarkOpen, now+50
	receive(phone, older)
	receive(phone, first)
	expect("older or replayed mark", protocol.TopicMarkArchived, 4, now+100)
	if n := count(t, w.alice, "topic_mark_copies WHERE carrier='' AND mark='archived'"); n != 1 {
		t.Fatalf("sender's own mark not recorded as present there: %d", n)
	}
	// Concurrent marks at one time converge by writer, whatever the order.
	low, high := "00000000-00000000-00000000-00000000", "ffffffff-ffffffff-ffffffff-ffffffff"
	for _, writer := range []string{high, low, high} {
		m := first
		m.At, m.Writer, m.Mark = now+200, writer, map[string]string{high: protocol.TopicMarkDone, low: protocol.TopicMarkOpen}[writer]
		receive(phone, m)
	}
	expect("concurrent writers", protocol.TopicMarkDone, 4, now+200)
	// A choice made here over that mark replaces it, even on a slower clock.
	if err = w.alice.changeTopicMark(conv, topic, protocol.TopicMarkOpen, 5); err != nil {
		t.Fatal(err)
	}
	expect("local choice over a newer clock", protocol.TopicMarkOpen, 5, now+201)
	clear := first
	clear.Mark, clear.Count, clear.At = protocol.TopicMarkNone, 0, now+300
	receive(phone, clear)
	expect("cleared mark", "", 0, 0)
	if id := receive(w.bob, protocol.TopicMark{Scope: conv, Topic: topic, Mark: protocol.TopicMarkDone, Count: 1, At: now + 400, Writer: w.bob.Self().Fingerprint()}); heldReason(t, w.alice, id) == "" {
		t.Fatal("another person's mark accepted")
	}
	expect("after another person's mark", "", 0, 0)
	if n := count(t, w.alice, "inbox WHERE sub='topic-state-sync'"); n != 0 {
		t.Fatalf("%d private marks became messages", n)
	}
}

func TestTopicMarkSyncOldReaderRestartAndSeed(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	conv, topic, bad, later := strings.Repeat("b", 64), protocol.NewID(), protocol.NewID(), protocol.NewID()
	// Marks set before marks synced (the store as an older release left it)
	// are the person's too; a malformed old row stays on this device.
	if _, err := w.alice.store.db.Exec(`DELETE FROM config WHERE k LIKE 'topic-marks-seeded/%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.store.db.Exec(`INSERT INTO topic_state(peer,topic,mark,mark_at,mark_count,updated_at) VALUES(?,?,'archived',1234,3,0),(?,?,'done',NULL,NULL,0)`, conv, topic, conv, bad); err != nil {
		t.Fatal(err)
	}
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	eventually(t, "phone session published", func() bool {
		return w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile) == nil && len(profile.Sessions) > 0
	})
	publish := func(ts int64, caps []string) {
		for _, session := range profile.Sessions { // every live session decides what the device reads
			r := protocol.CapsRecord{Address: phone.Address, Session: session, TS: ts, Caps: caps}
			r.Sign(phone.id.Sign)
			if e := phone.hub.do(tctx(t), "PUT", "/v1/caps", r, nil); e != nil {
				t.Fatal(e)
			}
		}
	}
	// The phone runs a release that reads names (own2) but not marks.
	publish(time.Now().Unix()+100, slices.DeleteFunc(slices.Clone(ownCaps), func(c string) bool { return c == protocol.CapTopicStateSync }))
	if _, err := w.alice.syncTopicMarks(); err != nil {
		t.Fatal(err)
	}
	carriers := func(a *Agent) int { return count(t, a, "outbox WHERE sub='topic-state-sync'") }
	if carriers(w.alice) != 1 {
		t.Fatalf("seeded mark made %d carriers", carriers(w.alice))
	}
	var encoded string
	if err := w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='topic-state-sync'`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	json.Unmarshal([]byte(encoded), &env)
	if r, e := w.alice.deliver(tctx(t), env, nil); e != nil || r.State != stateConvWaiting {
		t.Fatalf("older reader %+v %v", r, e)
	}
	// Later marks stay unsealed here instead of piling up for it.
	if err := w.alice.changeTopicMark(conv, later, protocol.TopicMarkArchived, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.syncTopicMarks(); err != nil {
		t.Fatal(err)
	}
	if carriers(w.alice) != 1 {
		t.Fatal("older reader collected another waiting carrier")
	}
	if err := w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err = again.syncTopicMarks(); err != nil {
		t.Fatal(err)
	}
	if carriers(again) != 1 || count(t, again, "topic_marks") != 2 {
		t.Fatalf("restart: %d carriers, %d marks", carriers(again), count(t, again, "topic_marks"))
	}
	if markAt(t, phone, conv, topic).Mark != "" {
		t.Fatal("older reader received a mark")
	}
	// It updates: the waiting carrier goes, then the marks held back.
	publish(time.Now().Unix()+200, slices.Clone(ownCaps))
	features, err := again.relayFeatures(tctx(t))
	if err != nil {
		t.Fatal(err)
	}
	again.releaseConv(tctx(t), features)
	if err = again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "seeded mark converges", func() bool {
		l := markAt(t, phone, conv, topic)
		return l.Mark == protocol.TopicMarkArchived && l.MarkCount == 3 && l.MarkAt == 1234
	})
	if _, err = again.syncTopicMarks(); err != nil {
		t.Fatal(err)
	}
	if err = again.FlushOutbox(tctx(t)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "held-back mark follows", func() bool { return markAt(t, phone, conv, later).Mark == protocol.TopicMarkArchived })
	if markAt(t, phone, conv, bad).Mark != "" {
		t.Fatal("malformed old mark left the device")
	}
	if _, err = again.syncTopicMarks(); err != nil || carriers(again) != 2 {
		t.Fatalf("delivered marks sent again: %d %v", carriers(again), err)
	}
	if err = again.store.setOutboxState(env.ID, stateQueued, "", ""); err != nil {
		t.Fatal(err)
	}
	if err = again.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if _, ok, e := again.mayDeliverTopicStateSync(env); e != nil || ok {
		t.Fatalf("removed reader delivered %v %v", ok, e)
	}
}

// A rename keeps a mark that arrived while it was made, as a mark keeps a
// name (TestTopicSyncLegacyRenameAndNonTitleChange).
func TestTopicRenameKeepsConcurrentMark(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	msg, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "Rename race"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.MarkTopicDone(w.bob.Address, msg.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err = w.alice.setTopic(w.bob.Address, msg.ID, func(l *topicLocal, n int) {
		if _, e := w.alice.ReopenTopic(w.bob.Address, msg.ID, 0); e != nil {
			t.Fatal(e)
		}
		l.Title = "Renamed meanwhile"
	}, true); err != nil {
		t.Fatal(err)
	}
	if l := markAt(t, w.alice, w.bob.Address, msg.ID); l.Mark != topicMarkOpen || l.Title != "Renamed meanwhile" {
		t.Fatalf("rename clobbered the newer mark: %+v", l)
	}
}
