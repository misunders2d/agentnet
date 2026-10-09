package client

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func TestChatTopicVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/topic_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Now       int64 `json:"now"`
		ChatCases []struct {
			Name     string
			Messages []json.RawMessage
			Local    map[string]topicLocal
			Assigned map[string]string
			Want     []struct {
				ID, State, DoneBy string
				Count             int
				Pending           bool
				PendingIDs        []string
			}
		} `json:"chat_cases"`
	}
	if err = json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.ChatCases) < 8 {
		t.Fatal("missing chat vectors")
	}
	for _, c := range v.ChatCases {
		t.Run(c.Name, func(t *testing.T) {
			msgs := []ConvMessage{}
			for _, raw := range c.Messages {
				var m ConvMessage
				var status struct{ Status string }
				json.Unmarshal(raw, &m)
				json.Unmarshal(raw, &status)
				m.status = status.Status
				msgs = append(msgs, m)
			}
			if got := ChatTopicAssignments(msgs); !reflect.DeepEqual(got, c.Assigned) {
				t.Fatalf("assignments got %v want %v", got, c.Assigned)
			}
			got := summarizeChatTopics("chat", msgs, c.Local, v.Now)
			if len(got) != len(c.Want) {
				t.Fatalf("topics %v want %v", got, c.Want)
			}
			for i, w := range c.Want {
				g := got[i]
				if g.ID != w.ID || g.State != w.State || g.DoneBy != w.DoneBy || g.Count != w.Count || g.Pending != w.Pending {
					t.Fatalf("got %+v want %+v", g, w)
				}
				if w.PendingIDs != nil && !reflect.DeepEqual(append([]string{}, g.PendingIDs...), w.PendingIDs) {
					t.Fatalf("pending IDs got %v want %v", g.PendingIDs, w.PendingIDs)
				}
			}
		})
	}
}

func testChatTopicJourney(t *testing.T, a, b *Agent, conv string) {
	t.Helper()
	ctx := tctx(t)
	send := func(from *Agent, m ConvOutgoing) ConvSent {
		r, e := from.SendConv(ctx, conv, m)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	topics := func(from *Agent) []ThreadSummary {
		r, e := from.ChatTopics(conv)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	wait := func(from *Agent, id string, count int, state string) {
		eventually(t, "topic converges "+state, func() bool {
			for _, x := range topics(from) {
				if x.ID == id {
					return x.Count == count && x.State == state
				}
			}
			return false
		})
	}
	main := send(a, ConvOutgoing{Body: "Main flow seed"})
	eventually(t, "main arrives", func() bool {
		_, n := convMsg(t, b, conv, func(m ConvMessage) bool { return m.LID == main.LID })
		return n == 1
	})
	send(b, ConvOutgoing{Body: "Ordinary reply stays main", ReplyTo: main.LID})
	eventually(t, "main reply arrives", func() bool { m, e := a.ConversationMessages(conv); return e == nil && len(m) >= 2 })
	if len(topics(a)) != 0 {
		t.Fatal("ordinary reply invented a topic")
	}
	if _, e := a.ChangeChatTopic(ctx, conv, main.LID, "create", "", 0); e != nil {
		t.Fatal(e)
	}
	wait(b, main.LID, 2, TopicActive)
	if covered, e := b.ChangeChatTopic(ctx, conv, main.LID, "done", "", 2); e != nil || !covered {
		t.Fatalf("shared mark %v %v", covered, e)
	}
	wait(a, main.LID, 2, TopicDone)
	if topics(a)[0].DoneBy != "person" {
		t.Fatal("shared action not attributed")
	}
	follow := send(a, ConvOutgoing{Body: "New message reopens", ReplyTo: main.LID})
	wait(b, main.LID, 3, TopicActive)
	m, n := convMsg(t, a, conv, func(m ConvMessage) bool { return m.LID == follow.LID })
	if n != 1 {
		t.Fatalf("follow-up copies: %d", n)
	}
	if m.Topic != main.LID {
		t.Fatalf("reply lost topic: %+v", m)
	}
	send(a, ConvOutgoing{Body: "Opt-in new topic", Topic: "new"})
	eventually(t, "two topic flows", func() bool { return len(topics(b)) == 2 })
	if _, e := a.ChangeChatTopic(ctx, conv, main.LID, "delete", "", 0); e != nil {
		t.Fatal(e)
	}
	if len(topics(a)) != 1 {
		t.Fatal("delete affected another topic or main")
	}
	if len(topics(b)) != 2 {
		t.Fatal("delete erased the other person's copy")
	}
	var freshTopic string
	for _, x := range topics(a) {
		freshTopic = x.ID
	}
	if _, e := a.ChangeChatTopic(ctx, conv, freshTopic, "archive", "", 0); e != nil {
		t.Fatal(e)
	}
	if topics(a)[0].State != TopicArchived || topics(b)[0].State == TopicArchived {
		t.Fatal("archive scope")
	}
	// A typed agent close is accepted only on a real completed agent turn.
	if e := envelope.CheckTopic(envelope.Inner{V: 2, Topic: freshTopic, TopicDone: true, Kind: "message"}); e == nil {
		t.Fatal("ordinary turn closes a topic")
	}
}
func TestChatTopicDMJourney(t *testing.T) {
	w, conv, _ := dmFiles(t)
	testChatTopicJourney(t, w.alice, w.bob, conv)
}
func TestChatTopicGroupJourney(t *testing.T) {
	w, _, g, _ := groupTurnsFixture(t)
	testChatTopicJourney(t, w.alice, w.bob, g.State.Conv)
}

func TestChatTopicHistoryAndOwnDeviceErasure(t *testing.T) {
	w, conv, _ := dmFiles(t)
	ctx := tctx(t)
	main, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Main stays"})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Topic retained", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	topics, err := w.alice.ChatTopics(conv)
	if err != nil || len(topics) != 1 {
		t.Fatalf("seed topics: %v %v", topics, err)
	}
	id := topics[0].ID
	// Match the two stale own-device badges: 19 and 4 stored incoming
	// copies, all displayed as this person's outgoing turns.
	for i := 1; i < 19; i++ {
		if _, err = w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Another own turn", Topic: id}); err != nil {
			t.Fatal(err)
		}
	}
	other, err := w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Other topic", Topic: "new"})
	if err != nil {
		t.Fatal(err)
	}
	otherTopic := convMsgByID(t, w.alice, conv, other.ID).Topic
	for i := 1; i < 4; i++ {
		if _, err = w.alice.SendConv(ctx, conv, ConvOutgoing{Body: "Other own turn", Topic: otherTopic}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = w.alice.ChangeChatTopic(ctx, conv, id, "done", "", 19); err != nil {
		t.Fatal(err)
	}
	original, err := w.alice.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	// Storage time may differ from the signed time; retained topics must
	// preserve their causal/lifecycle timestamps across a history snapshot.
	if _, err = w.alice.store.db.Exec(`UPDATE outbox SET created_at=created_at+60 WHERE conv=? AND topic IS NOT NULL`, conv); err != nil {
		t.Fatal(err)
	}
	phone := linked(t, w.alice)
	eventually(t, "closed topic on linked device", func() bool {
		ts, e := phone.ChatTopics(conv)
		if e != nil || len(ts) != 2 {
			return false
		}
		counts := map[string]int{}
		for _, topic := range ts {
			if topic.ID == id && topic.State != TopicDone {
				return false
			}
			counts[topic.ID] = topic.Count
		}
		return counts[id] == 19 && counts[otherTopic] == 4
	})
	retained, err := phone.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range original {
		if want.Topic == "" {
			continue
		}
		found := false
		for _, got := range retained {
			if got.LID == want.LID {
				found = true
				if got.Dir != "out" || got.Via != w.alice.Address {
					t.Fatalf("own-device history is not outgoing: %+v", got)
				}
				if got.Sent != want.Sent || got.Topic != want.Topic || !reflect.DeepEqual(got.TopicEvent, want.TopicEvent) {
					t.Fatalf("retained topic differs: got %+v want %+v", got, want)
				}
			}
		}
		if !found {
			t.Fatalf("missing retained turn %s", want.LID)
		}
	}
	assertUnread := func(want map[string]int) {
		t.Helper()
		ts, e := phone.ChatTopics(conv)
		if e != nil {
			t.Fatal(e)
		}
		got := map[string]int{}
		for _, topic := range ts {
			got[topic.ID] = topic.Unread
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("linked topic unread: got %v, want %v", got, want)
		}
	}
	assertUnread(map[string]int{id: 0, otherTopic: 0})
	// Genuine incoming turns remain unread. An exact read on one device
	// clears only that turn on the linked reader, not the newer unseen one.
	for _, body := range []string{"Read this foreign turn", "New unseen foreign turn"} {
		if _, err = w.bob.SendConv(ctx, conv, ConvOutgoing{Body: body, Topic: id}); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "foreign turns reach both own devices", func() bool {
		_, a := convMsg(t, w.alice, conv, func(m ConvMessage) bool { return m.From == w.bob.Address && m.Topic == id })
		_, p := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.From == w.bob.Address && m.Topic == id })
		return a == 2 && p == 2
	})
	assertUnread(map[string]int{id: 2, otherTopic: 0})
	readID, _ := readState(t, w.alice, conv, "Read this foreign turn")
	if err = w.alice.MarkRead([]string{readID}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "exact topic read reaches linked device", func() bool {
		_, read := readState(t, phone, conv, "Read this foreign turn")
		return read
	})
	if _, read := readState(t, phone, conv, "New unseen foreign turn"); read {
		t.Fatal("new unseen foreign turn marked read")
	}
	assertUnread(map[string]int{id: 1, otherTopic: 0})
	if _, err = w.alice.ChangeChatTopic(ctx, conv, id, "delete", "", 0); err != nil {
		t.Fatal(err)
	}
	eventually(t, "erased on own linked device", func() bool {
		msgs, e := phone.ConversationMessages(conv)
		return e == nil && len(msgs) == 5 && msgs[0].LID == main.LID
	})
	eventually(t, "other person retains topic", func() bool {
		_, n := convMsg(t, w.bob, conv, func(m ConvMessage) bool { return m.LID == seed.LID })
		return n == 1
	})
}

func TestChatTopicOwnAgentAnswerUnread(t *testing.T) {
	stub := installAgentStub(t)
	w, conv, _ := dmFiles(t)
	setResponder(t, w.alice, "agentstub", stub.dir, time.Minute)
	p, err := w.alice.InviteAgent(tctx(t), conv, w.alice.Address, nil, nil, "")
	if err != nil || !p.Claimable() {
		t.Fatalf("own agent: %+v %v", p, err)
	}
	q, err := w.alice.AskAgentInTopic(tctx(t), p.PID, envelope.KindQuestion, "Own agent topic", "new", nil)
	if err != nil {
		t.Fatal(err)
	}
	answer := replyAt(t, w.alice, conv, q.ID)
	phone := linked(t, w.alice)
	eventually(t, "own agent answer reaches linked device", func() bool {
		m, n := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == answer.LID })
		return n == 1 && m.VerifiedAgent
	})
	m, _ := convMsg(t, phone, conv, func(m ConvMessage) bool { return m.LID == answer.LID })
	if m.Kind != envelope.KindAnswer || m.Dir != "out" || m.Via != w.alice.Address {
		t.Fatalf("own agent answer direction: %+v", m)
	}
	topics, err := phone.ChatTopics(conv)
	if err != nil || len(topics) != 1 || topics[0].Count != 2 || topics[0].Unread != 0 {
		t.Fatalf("own agent topic unread: %+v %v", topics, err)
	}
}

// Topic metadata participates in logical-copy and selected-history commitments.
func TestChatTopicContentCommitment(t *testing.T) {
	in := envelope.Inner{V: 2, Conv: "chat", LID: "turn", Kind: envelope.KindMessage, Body: "unchanged"}
	base := contentHash(in)
	in.Topic = "flow"
	topic := contentHash(in)
	if topic == base {
		t.Fatal("topic not committed")
	}
	in.TopicEvent = &envelope.TopicEvent{Action: "done", Seen: []string{"turn"}}
	marked := contentHash(in)
	if marked == topic {
		t.Fatal("shared mark not committed")
	}
	in.TopicEvent.Seen = []string{"other"}
	if contentHash(in) == marked {
		t.Fatal("seen set not committed")
	}
	in.TopicEvent = nil
	in.Kind = envelope.KindAnswer
	in.Status = envelope.StatusDone
	answer := contentHash(in)
	in.TopicDone = true
	if contentHash(in) == answer {
		t.Fatal("explicit close not committed")
	}
}

func TestChatTopicGroupAgentReply(t *testing.T) {
	stub := installAgentStub(t)
	w, carol, packet, _ := groupTurnsFixture(t)
	setResponder(t, w.bob, "agentstub", stub.dir, 60*time.Second)
	conv := packet.State.Conv
	p := p6Member(t, w.alice, w.bob, conv)
	eventually(t, "third member holds group membership", func() bool { return stateAt(t, carol, p.PID).Claimable() })
	ask := func(topic string) ConvMessage {
		q, e := carol.AskAgentInTopic(tctx(t), p.PID, envelope.KindQuestion, "Please check this topic.", topic, nil)
		if e != nil {
			t.Fatal(e)
		}
		waitState(t, w.bob, q.LID, stateAwaiting)
		if e = w.bob.Accept(q.LID); e != nil {
			t.Fatal(e)
		}
		return replyAt(t, carol, conv, q.LID)
	}
	ordinary := ask("new")
	if ordinary.Topic == "" || ordinary.TopicDone {
		t.Fatalf("ordinary group reply: %+v", ordinary)
	}
	topics, e := carol.ChatTopics(conv)
	if e != nil || len(topics) != 1 || topics[0].State != TopicActive || topics[0].Pending {
		t.Fatalf("group topic: %+v %v", topics, e)
	}
	stub.mode("closed")
	done := ask(ordinary.Topic)
	if done.Topic != ordinary.Topic || !done.TopicDone {
		t.Fatalf("closed group reply: %+v", done)
	}
	topics, e = carol.ChatTopics(conv)
	if e != nil || len(topics) != 1 || topics[0].State != TopicDone || topics[0].DoneBy != DoneByAgent {
		t.Fatalf("finished group topic: %+v %v", topics, e)
	}
}

func TestChatMainPreferencesAndExactErasure(t *testing.T) {
	w, conv, _ := dmFiles(t)
	a := w.alice
	ctx := tctx(t)
	send := func(m ConvOutgoing) ConvMessage {
		t.Helper()
		r, err := a.SendConv(ctx, conv, m)
		if err != nil {
			t.Fatal(err)
		}
		return convMsgByID(t, a, conv, r.ID)
	}
	first := send(ConvOutgoing{Body: "Main original"})
	reply := send(ConvOutgoing{Body: "Main reply", ReplyTo: first.LID})
	seed := send(ConvOutgoing{Body: "Promoted seed"})
	child := send(ConvOutgoing{Body: "Promoted child", ReplyTo: seed.LID})
	if _, err := a.ChangeChatTopic(ctx, conv, seed.LID, "create", "", 0); err != nil {
		t.Fatal(err)
	}
	native := send(ConvOutgoing{Body: "Native topic", Topic: "new"})
	summary := func() *ThreadSummary {
		t.Helper()
		msgs, err := a.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		v, err := a.ChatMainTopicForMessages(conv, msgs)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	change := func(what, title string, count int) {
		t.Helper()
		if _, err := a.ChangeChatMainTopic(ctx, conv, what, title, count); err != nil {
			t.Fatal(err)
		}
	}
	if v := summary(); v == nil || v.ID != "" || v.Count != 2 {
		t.Fatalf("main: %+v", v)
	}
	change("rename", "Private Main", 2)
	if v := summary(); v.Title != "Private Main" || !v.Renamed {
		t.Fatalf("renamed: %+v", v)
	}
	third := send(ConvOutgoing{Body: "New Main message"})
	change("archive", "", 2)
	if v := summary(); v.State != TopicActive || v.Count != 3 {
		t.Fatalf("stale archive: %+v", v)
	}
	before, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	change("archive", "", 3)
	if v := summary(); v.State != TopicArchived {
		t.Fatalf("archive: %+v", v)
	}
	change("reopen", "", 3)
	if v := summary(); v.State != TopicActive {
		t.Fatalf("reopen: %+v", v)
	}
	automatic := summary().AutoTitle
	change("rename", "", 3)
	if v := summary(); v.Title != automatic || v.Renamed {
		t.Fatalf("reset: %+v", v)
	}
	after, err := a.ConversationMessages(conv)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) || !reflect.DeepEqual(ChatTopicAssignments(before), ChatTopicAssignments(after)) {
		t.Fatal("private preferences created public turns or assignments")
	}
	for _, what := range []string{"done", "create"} {
		if _, err := a.ChangeChatMainTopic(ctx, conv, what, "", 0); err == nil {
			t.Fatalf("root allowed %s", what)
		}
	}
	change("archive", "", 3)
	change("delete", "", 3)
	if v := summary(); v != nil {
		t.Fatalf("deleted main: %+v", v)
	}
	rows, err := a.store.db.Query(`SELECT key,lid FROM conv_erased WHERE conv=?`, conv)
	if err != nil {
		t.Fatal(err)
	}
	erased := map[string]string{}
	for rows.Next() {
		var key, lid string
		if err := rows.Scan(&key, &lid); err != nil {
			t.Fatal(err)
		}
		erased[lid] = key
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if !reflect.DeepEqual(erased, map[string]string{first.LID: a.Self().Fingerprint(), reply.LID: a.Self().Fingerprint(), third.LID: a.Self().Fingerprint()}) {
		t.Fatalf("erased wrong turns: %v", erased)
	}
	for _, m := range []ConvMessage{seed, child, native} {
		if got := convMsgByID(t, a, conv, m.ID); got.Body != m.Body {
			t.Fatalf("sibling changed: %+v", got)
		}
	}
	if _, _, ok, err := a.store.conversation(conv); err != nil || !ok {
		t.Fatalf("root changed: %v %v", ok, err)
	}
	topics, err := a.ChatTopics(conv)
	if err != nil || len(topics) != 2 {
		t.Fatalf("native siblings: %+v %v", topics, err)
	}
	send(ConvOutgoing{Body: "A new Main after deletion"})
	if v := summary(); v == nil || v.Count != 1 || v.State != TopicActive {
		t.Fatalf("new Main hidden by deleted archive: %+v", v)
	}
}
