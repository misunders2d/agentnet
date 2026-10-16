package client

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestChatUnreadPagingPositions(t *testing.T) {
	conv := strings.Repeat("c", 64)
	row := func(id, peer string, at int64, unread int) ThreadSummary {
		return ThreadSummary{Conv: conv, ID: id, Peer: peer, LastAt: time.Unix(at, 0), Unread: unread, State: TopicActive, Title: "searchable"}
	}
	all := []ThreadSummary{row("z", "a", 300, 0), row("b", "b", 100, 1), row("a", "a", 100, 1), row("b", "a", 100, 1), row("done", "a", 400, 0)}
	all[4].State = TopicDone
	page, err := chatTopicPage(all, TopicQuery{Conv: conv, Limit: 2, Query: " searchable "})
	if err != nil {
		t.Fatal(err)
	}
	if page.Matched != 5 || len(page.Topics) != 2 || page.Topics[0].ID != "b" || page.Topics[0].Peer != "a" || page.Topics[1].Peer != "b" {
		t.Fatalf("bucket/time/id/peer order: %+v", page)
	}
	cursor := page.Next
	// The cursor row moves to read and newer. Comparison keeps the old position.
	all[1].Unread = 0
	all[1].LastAt = time.Unix(1000, 0)
	moved, err := chatTopicPage(all, TopicQuery{Conv: conv, Before: cursor, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(moved.Topics) != 4 || moved.Topics[0].ID != "a" {
		t.Fatalf("moved cursor lost remaining unread: %+v", moved)
	}
	// Deletion cannot turn the same cursor into an error or lose the next row.
	all = slices.DeleteFunc(all, func(r ThreadSummary) bool { return r.ID == "b" && r.Peer == "b" })
	deleted, err := chatTopicPage(all, TopicQuery{Conv: conv, Before: cursor, Limit: 20})
	if err != nil || len(deleted.Topics) != 3 || deleted.Topics[0].ID != "a" {
		t.Fatalf("deleted cursor: %+v %v", deleted, err)
	}
	done, err := chatTopicPage(all, TopicQuery{Conv: conv, State: TopicDone})
	if err != nil || done.Matched != 1 || done.Topics[0].ID != "done" {
		t.Fatalf("filter lost: %+v %v", done, err)
	}
	for _, q := range []TopicQuery{{Conv: "foreign", Before: cursor}, {Conv: conv, Before: "20261016000000|" + conv + "|b"}, {Conv: conv, Before: "u1|2|1|" + conv + "||a"}, {Conv: conv, Limit: -1}} {
		if _, err := chatTopicPage(all, q); err == nil {
			t.Fatalf("accepted invalid %+v", q)
		}
	}
}

func TestDeviceUnreadPagingPositions(t *testing.T) {
	w := newWorld(t, "")
	if _, err := w.alice.CreatePerson(tctx(t), "Owner"); err != nil {
		t.Fatal(err)
	}
	peer := w.bob.Address
	var ids []string
	for i := 0; i < 4; i++ {
		r, err := w.alice.SendMessage(tctx(t), Outgoing{To: peer, Kind: envelope.KindMessage, Body: "Device paging"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	// Mark older topics unread. The complete set is sorted, not each page.
	for _, id := range ids[:2] {
		if err := w.alice.MarkUnread(id, []string{id}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := w.alice.Topics(TopicQuery{Peer: peer, Limit: 1})
	if err != nil || len(p.Topics) != 1 || p.Topics[0].Unread != 1 {
		t.Fatalf("unread first %+v %v", p, err)
	}
	first := p.Topics[0]
	refs, err := w.alice.Conversation(first.ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	generation := map[string]protocol.AttentionGeneration{}
	for _, m := range refs.Messages {
		if m.Attention != nil {
			generation[m.ID] = *m.Attention
		}
	}
	if err := w.alice.MarkReadWithAttention([]string{first.ID}, generation); err != nil {
		t.Fatal(err)
	}
	next, err := w.alice.Topics(TopicQuery{Peer: peer, Before: p.Next, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Topics) == 0 || next.Topics[0].Unread != 1 || next.Topics[0].ID == first.ID {
		t.Fatalf("read cursor lost unread successor %+v", next)
	}
	// Removing its local stored topic also keeps the strict-after position valid.
	if _, err := w.alice.DeleteThread(peer, first.ID); err != nil {
		t.Fatal(err)
	}
	next, err = w.alice.Topics(TopicQuery{Peer: peer, Before: p.Next, Limit: 10})
	if err != nil || len(next.Topics) == 0 || next.Topics[0].Unread != 1 {
		t.Fatalf("deleted device cursor %+v %v", next, err)
	}
	if _, err := w.alice.Topics(TopicQuery{Peer: "other/device", Before: p.Next}); err == nil {
		t.Fatal("cross-peer cursor accepted")
	}
}
