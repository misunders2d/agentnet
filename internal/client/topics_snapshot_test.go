package client

import (
	"reflect"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// An omitted archive's view can supply its exact agent's archive count without
// loading the overview. Another agent and ordinary human history stay separate.
func TestThreadTopicSnapshotKeepsExactArchivedGroup(t *testing.T) {
	w := newWorld(t, "")
	agent, hidden := protocol.NewID(), protocol.NewID()
	now := storeNow()
	put := func(agent, body, reply string, at time.Time, status string) string {
		t.Helper()
		id := protocol.NewID()
		kind := envelope.KindAnswer
		if agent == "" {
			kind = envelope.KindMessage
		}
		in := envelope.Inner{ID: id, From: w.bob.Address, To: w.alice.Address, Kind: kind,
			AgentID: agent, Body: body, ReplyTo: reply, TS: at.Unix(), Status: status}
		if err := w.alice.store.addInbox(in, w.bob.Self().Fingerprint()); err != nil {
			t.Fatal(err)
		}
		if _, err := w.alice.store.db.Exec(`UPDATE inbox SET received_at=?,received_ms=? WHERE id=?`, at.Unix(), at.UnixMilli(), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := now.Add(-12 * 24 * time.Hour)
	selected := put(agent, "Selected archive", "", old, "")
	sibling := put(agent, "Unseen sibling", selected, old.Add(time.Second), "")
	put(agent, "Independent archive", "", old.Add(time.Minute), "")
	latest := put(agent, "Current exact agent", "", now, "")
	put(hidden, "Different configured agent", "", old, "")
	put("", "Ordinary human history", "", old, "")
	put("", "Internal report", "", now.Add(time.Second), envelope.StatusReviewNotice)

	assertSnapshot := func(wantTotal, wantArchived, wantUnread int) {
		t.Helper()
		var snapshot PeerTopics
		selectedTopic, err := w.alice.TopicOf(selected, &snapshot)
		if err != nil {
			t.Fatal(err)
		}
		threads, groups, err := w.alice.TopicOverview(false)
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range threads {
			if thread.ID == selected {
				t.Fatal("fixture archive must be omitted from the overview")
			}
		}
		var expected *PeerTopics
		for i := range groups {
			if groups[i].Latest.AgentID == agent {
				expected = &groups[i]
			}
		}
		if selectedTopic.State != TopicArchived || snapshot.Peer != w.bob.Address || snapshot.Total != wantTotal || snapshot.Archived != wantArchived ||
			snapshot.ArchivedUnread != wantUnread || snapshot.Latest.ID != latest || snapshot.Latest.Title != "Current exact agent" ||
			expected == nil || !reflect.DeepEqual(snapshot, *expected) {
			t.Fatalf("selected=%+v snapshot=%+v overview=%+v", selectedTopic, snapshot, groups)
		}
	}
	assertSnapshot(3, 2, 3)
	if err := w.alice.MarkRead([]string{selected}); err != nil {
		t.Fatal(err)
	}
	assertSnapshot(3, 2, 2)
	if got := topicOf(t, w.alice, sibling); got.Unread != 1 {
		t.Fatalf("unseen sibling lost its unread state: %+v", got)
	}
	put(agent, "Later independent archive", "", old.Add(2*time.Minute), "")
	assertSnapshot(4, 3, 3)
}
