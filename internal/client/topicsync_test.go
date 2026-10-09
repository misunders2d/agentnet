package client

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func titleAt(t *testing.T, a *Agent, scope, topic string) string {
	t.Helper()
	l, err := a.store.topicLocals(scope)
	if err != nil {
		t.Fatal(err)
	}
	return l[topic].Title
}

func TestTopicSyncOwnHumanRenameJourney(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	conv := newDM(t, w.alice, w.bob)
	seed, err := w.alice.SendConv(tctx(t), conv, ConvOutgoing{Body: "Automatic topic name", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	var topic string
	eventually(t, "initial topic arrives before rename", func() bool {
		ts, e := phone.ChatTopics(conv)
		if e != nil || len(ts) != 1 {
			return false
		}
		topic = ts[0].ID
		return true
	})
	eventually(t, "initial history job finished before rename", func() bool {
		var state string
		return w.alice.store.db.QueryRow(`SELECT state FROM history_jobs WHERE device=?`, phone.Address).Scan(&state) == nil && state == "done"
	})
	if topic == "" || seed.LID == "" {
		t.Fatal("missing topic")
	}
	if _, err = w.alice.ChangeChatTopic(tctx(t), conv, topic, "rename", "A shared private name", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "own phone title renamed", func() bool { return titleAt(t, phone, conv, topic) == "A shared private name" })
	if titleAt(t, w.bob, conv, topic) != "" {
		t.Fatal("private title leaked to another person")
	}
	if _, err = phone.ChangeChatTopic(tctx(t), conv, topic, "rename", "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "reset returns laptop to automatic title", func() bool {
		ts, e := w.alice.ChatTopics(conv)
		return e == nil && len(ts) == 1 && ts[0].Title == "Automatic topic name" && !ts[0].Renamed
	})
	if n := count(t, w.alice, "inbox WHERE sub='topic-sync'"); n != 0 {
		t.Fatalf("%d private controls became chat messages", n)
	}
}

func TestTopicSyncReorderedBeforeHistoryAndAuthority(t *testing.T) {
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
	conv := strings.Repeat("a", 64)
	topic := protocol.NewID()
	base := protocol.TopicSync{V: 1, Person: me.info.Person, Roster: me.info.Roster, Titles: []protocol.TopicTitle{{Scope: conv, Topic: topic, Title: "new name", Rev: 4, Writer: phone.Self().Fingerprint()}}}
	receive := func(from *Agent, r protocol.TopicSync) string {
		t.Helper()
		raw, _ := json.Marshal(r)
		env := craft(t, from, w.alice, envelope.Inner{Kind: envelope.KindMessage, Sub: envelope.SubTopicSync, Replica: true, Body: string(raw)})
		if e := w.alice.verifyAndStore(tctx(t), env); e != nil {
			t.Fatal(e)
		}
		return env.ID
	}
	// A first explicit reset also beats an older device's late migration seed.
	resetTopic := protocol.NewID()
	if err = w.alice.setTopicTitle(conv, resetTopic, ""); err != nil {
		t.Fatal(err)
	}
	seed := base
	seed.Titles = []protocol.TopicTitle{{Scope: conv, Topic: resetTopic, Title: "Old saved name", Rev: 1, Writer: "ffffffff-ffffffff-ffffffff-ffffffff"}}
	receive(phone, seed)
	if titleAt(t, w.alice, conv, resetTopic) != "" {
		t.Fatal("late pre-upgrade seed overrode explicit reset")
	}
	receive(phone, base)
	if titleAt(t, w.alice, conv, topic) != "new name" {
		t.Fatal("pre-history name lost")
	}
	old := base
	old.Titles = append([]protocol.TopicTitle(nil), base.Titles...)
	old.Titles[0].Rev = 3
	old.Titles[0].Title = "stale"
	receive(phone, old)
	receive(phone, base)
	if titleAt(t, w.alice, conv, topic) != "new name" {
		t.Fatal("old/replayed title overwrote latest")
	}
	conflict := base
	conflict.Titles = append([]protocol.TopicTitle(nil), base.Titles...)
	conflict.Titles[0].Title = "conflict"
	if id := receive(phone, conflict); heldReason(t, w.alice, id) == "" {
		t.Fatal("conflicting exact revision accepted")
	}
	if id := receive(w.bob, base); heldReason(t, w.alice, id) == "" {
		t.Fatal("another person's title accepted")
	}
	// Independent concurrent writers converge by revision then fingerprint.
	low, high := "00000000-00000000-00000000-00000000", "ffffffff-ffffffff-ffffffff-ffffffff"
	for _, writer := range []string{high, low, high} {
		r := base
		r.Titles = append([]protocol.TopicTitle(nil), base.Titles...)
		r.Titles[0].Rev = 5
		r.Titles[0].Writer = writer
		r.Titles[0].Title = writer
		receive(phone, r)
	}
	if titleAt(t, w.alice, conv, topic) != high {
		t.Fatal("concurrent writer ordering failed")
	}
	if err = w.alice.setTopicTitle(conv, topic, ""); err != nil {
		t.Fatal(err)
	}
	var rev int64
	if err = w.alice.store.db.QueryRow(`SELECT revision FROM topic_titles WHERE scope=? AND topic=?`, conv, topic).Scan(&rev); err != nil || rev != 6 {
		t.Fatalf("causal local reset rev=%d err=%v", rev, err)
	}
	receive(phone, base)
	if titleAt(t, w.alice, conv, topic) != "" {
		t.Fatal("reset resurrected by old carrier")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = topicSyncAuthority(w.alice.store.db, base, phone.Address, phone.Self().Fingerprint(), w.alice.Address, w.alice.Self().Fingerprint()); err == nil {
		t.Fatal("pending pin supplied topic authority")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	original, _ := json.Marshal(me.roster)
	me.roster.HumanKeys = []string{w.alice.Self().Fingerprint()}
	raw, _ := json.Marshal(me.roster)
	if _, err = w.alice.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(raw), personSelf); err != nil {
		t.Fatal(err)
	}
	if err = topicSyncAuthority(w.alice.store.db, base, phone.Address, phone.Self().Fingerprint(), w.alice.Address, w.alice.Self().Fingerprint()); err == nil {
		t.Fatal("agent-host supplied topic authority")
	}
	if _, err = w.alice.store.db.Exec(`UPDATE persons SET record=? WHERE state=?`, string(original), personSelf); err != nil {
		t.Fatal(err)
	}
}

func TestTopicSyncMigrationRestartOldReaderAndKeyFence(t *testing.T) {
	w := newWorld(t, "")
	stop := runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	stop()
	conv := strings.Repeat("b", 64)
	topic := protocol.NewID()
	if _, err := w.alice.store.db.Exec(`INSERT INTO topic_state(peer,topic,title,mark,updated_at) VALUES(?,?,?,'done',0)`, conv, topic, "Saved before upgrade"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < protocol.MaxTopicTitles; i++ {
		if _, err := w.alice.store.db.Exec(`INSERT INTO topic_state(peer,topic,title,updated_at) VALUES(?,?,?,0)`, conv, protocol.NewID(), "Another saved title"); err != nil {
			t.Fatal(err)
		}
	}
	more, err := w.alice.syncTopicTitles()
	if err != nil || !more {
		t.Fatalf("first bounded migration %v %v", more, err)
	}
	more, err = w.alice.syncTopicTitles()
	if err != nil || more {
		t.Fatalf("second bounded migration %v %v", more, err)
	}
	if count(t, w.alice, "outbox WHERE sub='topic-sync'") != 2 {
		t.Fatal("65 saved titles did not make exactly two carriers")
	}
	var encoded string
	if err = w.alice.store.db.QueryRow(`SELECT envelope FROM outbox WHERE sub='topic-sync' ORDER BY rowid LIMIT 1`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var env envelope.Envelope
	json.Unmarshal([]byte(encoded), &env)
	var profile protocol.Profile
	label, device, _ := protocol.SplitAddress(phone.Address)
	if err = w.alice.hub.do(tctx(t), "GET", "/v1/agents/"+label+"/"+device+"/profile", nil, &profile); err != nil {
		t.Fatal(err)
	}
	publish := func(ts int64, caps []string) {
		r := protocol.CapsRecord{Address: phone.Address, Session: profile.Sessions[0], TS: ts, Caps: caps}
		r.Sign(phone.id.Sign)
		if e := phone.hub.do(tctx(t), "PUT", "/v1/caps", r, nil); e != nil {
			t.Fatal(e)
		}
	}
	publish(time.Now().Unix()+100, []string{protocol.CapEnv2, protocol.CapPerson, protocol.CapRoom})
	if r, e := w.alice.deliver(tctx(t), env, nil); e != nil || r.State != stateConvWaiting {
		t.Fatalf("old reader %+v %v", r, e)
	}
	if err = w.alice.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err = again.syncTopicTitles(); err != nil {
		t.Fatal(err)
	}
	if count(t, again, "outbox WHERE sub='topic-sync'") != 2 {
		t.Fatal("restart duplicated current titles")
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
	eventually(t, "legacy saved title converges", func() bool { return titleAt(t, phone, conv, topic) == "Saved before upgrade" })
	if err = again.store.setOutboxState(env.ID, stateQueued, "", ""); err != nil {
		t.Fatal(err)
	}
	transport := again.hub.http.Transport
	changed := false
	again.hub.http.Transport = workspaceRealmTransport(func(req *http.Request) (*http.Response, error) {
		response, e := transport.RoundTrip(req)
		if e == nil && strings.HasSuffix(req.URL.Path, "/profile") && !changed {
			changed = true
			_, e = again.store.db.Exec(`UPDATE peers SET pending=public WHERE address=?`, phone.Address)
		}
		return response, e
	})
	_, allowed, deliveryErr := again.mayDeliverTopicSync(env)
	again.hub.http.Transport = transport
	if deliveryErr != nil || allowed || !changed {
		t.Fatalf("post-profile key race allowed=%v changed=%v err=%v", allowed, changed, deliveryErr)
	}
	if _, err = again.store.db.Exec(`UPDATE peers SET pending=NULL WHERE address=?`, phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = again.RemoveDevice(tctx(t), phone.Address); err != nil {
		t.Fatal(err)
	}
	if err = again.store.setOutboxState(env.ID, stateQueued, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, e := again.mayDeliverTopicSync(env); e != nil || ok {
		t.Fatalf("removed reader delivered %v %v", ok, e)
	}
}

func TestTopicSyncLegacyRenameAndNonTitleChange(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	phone := linked(t, w.alice)
	msg, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "Legacy topic"})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.RenameTopic(w.bob.Address, msg.ID, "Legacy private title"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "legacy title reaches own phone", func() bool { return titleAt(t, phone, w.bob.Address, msg.ID) == "Legacy private title" })
	// Reproduce a remote title arriving after mark reads the local row, before
	// its transaction. The mark must preserve the independently newer title.
	if err = w.alice.setTopic(w.bob.Address, msg.ID, func(l *topicLocal, n int) {
		if e := w.alice.setTopicTitle(w.bob.Address, msg.ID, "Arrived during mark"); e != nil {
			t.Fatal(e)
		}
		l.Mark, l.MarkCount = topicMarkDone, n
	}); err != nil {
		t.Fatal(err)
	}
	if titleAt(t, w.alice, w.bob.Address, msg.ID) != "Arrived during mark" {
		t.Fatal("mark clobbered newer synced title")
	}
	locals, err := w.alice.store.topicLocals(w.bob.Address)
	if err != nil || locals[msg.ID].Mark != topicMarkDone {
		t.Fatal("mark was lost", err)
	}
	if err = w.alice.RenameTopic(w.bob.Address, msg.ID, ""); err != nil {
		t.Fatal(err)
	}
	eventually(t, "legacy reset reaches own phone", func() bool { return titleAt(t, phone, w.bob.Address, msg.ID) == "" })
	threads, _, err := w.alice.TopicOverview(true)
	if err != nil {
		t.Fatal(err)
	}
	for _, thread := range threads {
		if thread.Peer == phone.Address {
			t.Fatal("quiet private carriers became device topics")
		}
	}
}
