package client

import "testing"

// Deleting a device chat takes its archived topics too (owner 2026-11-27:
// the reported row was three archived one-message topics; "delete removes
// the whole chat"). One delete leaves no row, not even an archived count.
func TestDeleteDeviceChatTakesArchivedTopics(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	var ids []string
	for _, body := range []string{"oct 18", "oct 24", "today"} {
		sent, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sent.ID)
	}
	eventually(t, "three topics here", func() bool { ts, _ := w.alice.Threads(); return len(ts) == 3 })
	// Two topics quiet for over TopicArchiveAfter: archived by the clock.
	if _, err := w.alice.store.db.Exec(`UPDATE inbox SET received_at = received_at - 8*86400 WHERE id IN (?, ?)`, ids[0], ids[1]); err != nil {
		t.Fatal(err)
	}
	active, _, err := w.alice.TopicOverview(false)
	if err != nil || len(active) != 1 {
		t.Fatalf("active topics: %d %v", len(active), err)
	}
	if _, rows, err := w.alice.TopicOverview(true); err != nil || len(rows) != 1 || rows[0].Total != 3 || rows[0].Archived != 2 {
		t.Fatalf("chat row before: %+v %v", rows, err)
	}
	done, err := w.alice.DeleteDeviceChat(w.bob.Address, ids[2])
	if err != nil || done.Erased != 3 {
		t.Fatalf("delete chat: %+v %v", done, err)
	}
	ts, rows, err := w.alice.TopicOverview(true)
	if err != nil || len(ts) != 0 || len(rows) != 0 {
		t.Fatalf("left after deleting the chat: %+v %+v %v", ts, rows, err)
	}
	if theirs, err := w.bob.Threads(); err != nil || len(theirs) != 3 {
		t.Fatalf("bob's copy: %d %v", len(theirs), err)
	}
}
