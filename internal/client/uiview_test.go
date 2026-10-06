package client

import (
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

func waitClosed(t *testing.T, what string, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("no change signalled after %s", what)
	}
}

func threadsWith(t *testing.T, a *Agent, peer string) map[string]ThreadSummary {
	t.Helper()
	ts, err := a.Threads()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ThreadSummary{}
	for _, s := range ts {
		if s.Peer == peer {
			out[s.ID] = s
		}
	}
	return out
}

// Threads are the reply-linked groups of one peer's messages; the change
// feed fires for the daemon's own writes and for writes made by another
// agentnet process on the same home.
func TestThreadsAndChangeFeed(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	alice := w.alice.Address

	_, ch := w.bob.Changed()
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "which port?\nsecond line"})
	if err != nil {
		t.Fatal(err)
	}
	waitClosed(t, "a question arrived", ch)
	m, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "unrelated note"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "two threads", func() bool { return len(threadsWith(t, w.bob, alice)) == 2 })
	ts := threadsWith(t, w.bob, alice)
	qt, mt := ts[q.ID], ts[m.ID]
	if qt.Title != "which port?" || qt.Review != 1 || qt.Unread != 1 || qt.Count != 1 || mt.Count != 1 || mt.Review != 0 {
		t.Fatalf("threads %+v", ts)
	}
	if !mt.LastAt.After(qt.LastAt) && !mt.LastAt.Equal(qt.LastAt) {
		t.Fatalf("order %v %v", qt.LastAt, mt.LastAt)
	}

	// A reply joins its thread; the question no longer waits for bob.
	if _, err := w.bob.Reply(tctx(t), q.ID, "8443"); err != nil {
		t.Fatal(err)
	}
	ts = threadsWith(t, w.bob, alice)
	if qt := ts[q.ID]; len(ts) != 2 || qt.Count != 2 || qt.Review != 0 || qt.Last != "8443" {
		t.Fatalf("after reply %+v", ts)
	}
	// Alice's copy: the question she sent waits until the answer arrives.
	if at := threadsWith(t, w.alice, w.bob.Address)[q.ID]; !at.Waiting {
		t.Fatalf("alice's question not waiting: %+v", at)
	}

	// Another process on bob's home marks the message read: the daemon's
	// feed hears it through the local socket, and the counter survives.
	seq, ch := w.bob.Changed()
	other, err := Open(w.bobHome)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err := other.MarkRead([]string{m.ID}); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, "another process wrote", ch)
	if now, _ := w.bob.Changed(); now <= seq {
		t.Fatalf("seq %d -> %d", seq, now)
	}
	if mt := threadsWith(t, w.bob, alice)[m.ID]; mt.Unread != 0 {
		t.Fatalf("still unread: %+v", mt)
	}
}

// A review notice is a report from another machine: it is counted apart
// from decisions here and from unread messages, and its thread is marked.
func TestThreadsCountNoticesApart(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	alice := w.alice.Address
	n, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage,
		Status: envelope.StatusReviewNotice, Body: "2 request(s) wait"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "both arrive", func() bool { return len(threadsWith(t, w.bob, alice)) == 2 })
	ts := threadsWith(t, w.bob, alice)
	if nt := ts[n.ID]; !nt.NoticeOnly || nt.Notices != 1 || nt.Review != 0 || nt.Unread != 0 {
		t.Fatalf("notice thread %+v", nt)
	}
	if mt := ts[m.ID]; mt.NoticeOnly || mt.Notices != 0 || mt.Unread != 1 {
		t.Fatalf("message thread %+v", mt)
	}
	if err := w.bob.Resolve(n.ID); err != nil {
		t.Fatal(err)
	}
	if nt := threadsWith(t, w.bob, alice)[n.ID]; nt.Notices != 0 || !nt.NoticeOnly {
		t.Fatalf("after dismiss %+v", nt)
	}
}

// Only the exact shape the store files as a review notice counts as one: a
// reply or a message with files carrying the same status is a normal,
// unread message. A notice that got a reply stays counted and reachable.
func TestThreadsNoticeShapeIsExact(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.bob)
	alice := w.alice.Address
	send := func(o Outgoing) string {
		t.Helper()
		o.To, o.Kind = w.bob.Address, envelope.KindMessage
		res, err := w.alice.SendMessage(tctx(t), o)
		if err != nil {
			t.Fatal(err)
		}
		return res.ID
	}
	first := send(Outgoing{Body: "hello"})
	send(Outgoing{Body: "looks like a notice", ReplyTo: first, Status: envelope.StatusReviewNotice})
	path, _ := writeFile(t, t.TempDir(), "report.txt", 64)
	withFile := send(Outgoing{Body: "also looks like one", Files: []string{path}, Status: envelope.StatusReviewNotice})
	notice := send(Outgoing{Body: "1 request(s) wait", Status: envelope.StatusReviewNotice})
	eventually(t, "all arrive", func() bool { return len(threadsWith(t, w.bob, alice)) == 3 })
	if _, err := w.bob.Reply(tctx(t), notice, "seen, thanks"); err != nil {
		t.Fatal(err)
	}
	ts := threadsWith(t, w.bob, alice)
	if c := ts[first]; c.Count != 2 || c.Unread != 2 || c.Notices != 0 || c.NoticeOnly {
		t.Fatalf("reply with notice status %+v", c)
	}
	if c := ts[withFile]; c.Unread != 1 || c.Notices != 0 || c.NoticeOnly {
		t.Fatalf("files with notice status %+v", c)
	}
	if c := ts[notice]; c.Count != 2 || c.Notices != 1 || c.NoticeOnly || c.Review != 0 || c.Unread != 0 {
		t.Fatalf("notice with a reply %+v", c)
	}
}

// The page's views are device history: DM messages, sent or received, never
// become device threads, unread counts or device review items, also with a
// device thread beside them; a DM question for the person is listed apart
// (held), with its conversation, never as a decision. agentnet inbox's
// review keeps them, and viewing marks nothing read.
func TestPageViewsLeaveOutConversations(t *testing.T) {
	w := newWorld(t, "")
	runAgent(t, w.alice)
	runAgent(t, w.bob)
	persons(t, w.alice, w.bob)
	alice := w.alice.Address
	c1, c2 := newDM(t, w.alice, w.bob), newDM(t, w.alice, w.bob)
	if _, err := w.alice.SendConv(tctx(t), c1, ConvOutgoing{Body: "dm deploy topic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.SendConv(tctx(t), c2, ConvOutgoing{Kind: envelope.KindQuestion, Body: "dm budget question?"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob to hold both DMs", func() bool {
		return len(convBodies(t, w.bob, c1)) == 1 && len(convBodies(t, w.bob, c2)) == 1
	})
	if _, err := w.bob.SendConv(tctx(t), c1, ConvOutgoing{Body: "dm reply from bob"}); err != nil {
		t.Fatal(err)
	}
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindQuestion, Body: "device question?"})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the DM reply at alice and the device question at bob", func() bool {
		return len(convBodies(t, w.alice, c1)) == 2 && len(threadsWith(t, w.bob, alice)) == 1
	})

	for who, a := range map[string]*Agent{"alice": w.alice, "bob": w.bob} {
		ts, err := a.Threads()
		if err != nil {
			t.Fatal(err)
		}
		if len(ts) != 1 || ts[0].ID != q.ID || ts[0].Count != 1 || ts[0].Title != "device question?" {
			t.Errorf("%s's page threads: %+v", who, ts)
		}
	}
	if bt := threadsWith(t, w.bob, alice)[q.ID]; bt.Unread != 1 || bt.Review != 1 {
		t.Errorf("bob's device thread counts DM messages: %+v", bt)
	}
	page, err := w.bob.PageReview()
	if err != nil || len(page.Device) != 1 || page.Device[0].ID != q.ID || len(page.Conv) != 0 {
		t.Fatalf("page review: %v %+v", err, page)
	}
	if len(page.Held) != 1 || page.Held[0].Reason != ReviewHeldTurn || page.Held[0].Conv != c2 || page.Held[0].Body != "dm budget question?" || page.Held[0].From != alice {
		t.Fatalf("page review's held DM question: %+v", page.Held)
	}
	all, err := w.bob.Review()
	held := 0
	for _, m := range all {
		if m.State == stateConvHeld {
			held++
		}
	}
	if err != nil || len(all) != 2 || held != 1 {
		t.Fatalf("agentnet inbox review lost the DM question: %v %+v", err, all)
	}
	if n := inboxCount(t, w.bob, `conv IS NOT NULL AND read_at IS NULL`); n != 2 {
		t.Fatalf("%d unread DM messages at bob, want 2 (viewing marks nothing read)", n)
	}
}

// MEL-562: sent internal review reports must not become person-visible topics.
func TestSentReviewNoticeIsNotConversationTopic(t *testing.T) {
	w := newWorld(t, "")
	notice, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Status: envelope.StatusReviewNotice, Body: `{"v":2,"items":[]}`})
	if err != nil {
		t.Fatal(err)
	}
	ordinary, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Kind: envelope.KindMessage, Body: `{"user":"ordinary JSON remains visible"}`})
	if err != nil {
		t.Fatal(err)
	}
	threads := threadsWith(t, w.alice, w.bob.Address)
	if !threads[notice.ID].NoticeOnly || threads[notice.ID].Notices != 0 {
		t.Fatalf("outgoing internal report became topic: %+v", threads[notice.ID])
	}
	if threads[ordinary.ID].NoticeOnly {
		t.Fatal("ordinary user JSON hidden as report")
	}
	page, err := w.alice.Topics(TopicQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, topic := range page.Topics {
		if topic.ID == notice.ID {
			t.Fatal("internal report appeared in All topics")
		}
	}
}
