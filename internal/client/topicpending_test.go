package client

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// Old own-history rows need no migration or execution transition to explain
// Waiting. Reopening the store and receiving an exact late reply rederive it.
func TestChatTopicPendingHistoryRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.db.Close() }()
	add := func(id, lid, kind, reply, status string, at int64) {
		t.Helper()
		_, err := s.db.Exec(`INSERT INTO inbox(id,lid,sender,ts,kind,body,reply_to,status,received_at,received_ms,conv,topic,claimed_fp,via)
			VALUES(?,?,'author/laptop',?,?,'synthetic retained history',?,?,?,?,'chat','sandbox','original-key','own/laptop')`, id, lid, at, kind, reply, status, at, at*1000)
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func(want []string) {
		t.Helper()
		msgs, err := s.convMessages("chat", "own/phone", "own-key", nil)
		if err != nil {
			t.Fatal(err)
		}
		ts := summarizeChatTopics("chat", msgs, nil, 1000000)
		if len(ts) != 1 || ts[0].Pending != (len(want) > 0) || !reflect.DeepEqual(ts[0].PendingIDs, want) {
			t.Fatalf("pending summary %+v want exact IDs %v", ts, want)
		}
		var changed int
		if err := s.db.QueryRow(`SELECT count(*) FROM inbox WHERE coalesce(state,'')!=''`).Scan(&changed); err != nil || changed != 0 {
			t.Fatalf("reading topics changed execution states: %d %v", changed, err)
		}
	}
	add("first-copy", "first", "task", "", "", 1)
	add("second-copy", "second", "question", "", "", 2)
	add("ordinary-reply", "ordinary", "message", "first", "", 3)
	add("progress-copy", "progress", "answer", "first", "progress", 4)
	add("second-answer", "second-result", "answer", "second", "done", 5)
	check([]string{"first-copy"})
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	check([]string{"first-copy"})
	add("late-final", "late-result", "result", "first", "done", 6)
	check(nil)
}

func TestLegacyTopicNamesEveryExactPendingItem(t *testing.T) {
	rows := map[string]threadRow{
		"question": {link: link{id: "question", at: 1}, kind: "question", state: "delivered"},
		"answer":   {link: link{id: "answer", replyTo: "question", at: 2}, kind: "answer", status: "done", in: true},
		"task":     {link: link{id: "task", at: 3}, kind: "task", state: "delivered"},
		"review":   {link: link{id: "review", at: 4}, kind: "message", state: "needs_human", in: true, notice: true},
	}
	topic := summarize("own/laptop", "agent/host", []string{"question", "answer", "task", "review"}, rows, topicLocal{}, 10)
	if !topic.Pending || !reflect.DeepEqual(topic.PendingIDs, []string{"task", "review"}) {
		t.Fatalf("exact pending items %+v", topic)
	}
}

// Real persisted local jobs are outgoing presentation rows. Their durable
// terminal state settles Pending without rewriting direction or job state.
func TestChatTopicPendingPersistedLocalJobs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	s, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.db.Close() }()
	states := []string{"resolved", "cancelled", "answered", "not_run", "running", "accepted", "needs_human", "interrupted", "cancel_requested"}
	var want []string
	for i, state := range states {
		id := fmt.Sprintf("%032x", i+1)
		_, err := s.db.Exec(`INSERT INTO outbox(id,lid,recipient,kind,body,envelope,state,created_at,created_ms,conv,topic) VALUES(?,?,'own/laptop','task','retained local request','{"ts":1}','delivered',1,?,'chat','sandbox')`, id, id, i+1)
		if err != nil {
			t.Fatal(err)
		}
		_, err = s.db.Exec(`INSERT INTO inbox(id,lid,sender,ts,kind,body,received_at,received_ms,conv,topic,pid,local,state) VALUES(?,?,'own/laptop',1,'task','retained local request',1,?,'chat','sandbox','pid',1,?)`, id, id, i+1, state)
		if err != nil {
			t.Fatal(err)
		}
		if i >= 4 {
			want = append(want, id)
		}
	}
	check := func() {
		t.Helper()
		msgs, err := s.convMessages("chat", "own/laptop", "own-key", nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != len(states) {
			t.Fatalf("got %d messages", len(msgs))
		}
		for _, m := range msgs {
			if m.Dir != "out" || m.Job == "" {
				t.Fatalf("lost outgoing local job: %+v", m)
			}
		}
		ts := summarizeChatTopics("chat", msgs, nil, 1000000)
		if len(ts) != 1 || !reflect.DeepEqual(ts[0].PendingIDs, want) {
			t.Fatalf("pending %+v want %v", ts, want)
		}
		for i, state := range states {
			var got string
			if err := s.db.QueryRow(`SELECT state FROM inbox WHERE id=?`, fmt.Sprintf("%032x", i+1)).Scan(&got); err != nil || got != state {
				t.Fatalf("projection changed state %s to %s: %v", state, got, err)
			}
		}
	}
	check()
	if err := s.db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	check()
}
