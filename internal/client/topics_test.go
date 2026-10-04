package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
)

// topicVectors are the shared derivation vectors (testdata/topic_vectors.json):
// this package and engine.mjs (internal/ui topics_browser_test.go) both
// derive exactly these.
type topicVectors struct {
	Now          int64 `json:"now"`
	ArchiveAfter int64 `json:"archive_after"`
	Cases        []struct {
		Name     string `json:"name"`
		Messages []struct {
			ID       string `json:"id"`
			Dir      string `json:"dir"`
			Kind     string `json:"kind"`
			State    string `json:"state"`
			Status   string `json:"status"`
			ReplyTo  string `json:"reply_to"`
			At       int64  `json:"at"`
			Notice   bool   `json:"notice"`
			Selected bool   `json:"selected"`
		} `json:"messages"`
		Local topicLocal   `json:"local"`
		Want  topicVerdict `json:"want"`
	} `json:"cases"`
}

func TestTopicDerivationVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/topic_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v topicVectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatal(err)
	}
	if v.ArchiveAfter != int64(TopicArchiveAfter/time.Second) {
		t.Fatalf("vectors assume archiving after %ds, TopicArchiveAfter is %v: update the vectors with the constant", v.ArchiveAfter, TopicArchiveAfter)
	}
	if len(v.Cases) < 20 {
		t.Fatalf("only %d vectors", len(v.Cases))
	}
	for _, c := range v.Cases {
		var g []threadRow
		for _, m := range c.Messages {
			g = append(g, threadRow{link: link{id: m.ID, replyTo: m.ReplyTo, at: m.At}, in: m.Dir == "in", kind: m.Kind, state: m.State,
				status: m.Status, notice: m.Notice, selected: m.Selected})
		}
		if got := deriveTopic(g, c.Local, v.Now); got != c.Want {
			t.Errorf("%s:\n got %+v\nwant %+v", c.Name, got, c.Want)
		}
	}
}

// topicOf finds the topic that message id belongs to, as a reads it now.
func topicOf(t *testing.T, a *Agent, id string) ThreadSummary {
	t.Helper()
	ts, err := a.TopicOf(id)
	if err != nil {
		t.Fatalf("topic of %s: %v", id, err)
	}
	return ts
}

// Topics on real installations: the agent's final result makes a topic
// done (failed or hand-written answers do not), Mark done / Reopen / a name
// are kept here and survive reopening the home, a new message ends a mark,
// quiet topics are archived (never one that waits) and the overview then
// counts them instead of listing them.
func TestTopicLifecycle(t *testing.T) {
	t.Cleanup(func() { testShift.Store(0) })
	w := newWorld(t, "")
	runAgent(t, w.bob)
	runAgent(t, w.alice)
	bob := w.bob.Address
	send := func(kind, body, replyTo string) string {
		t.Helper()
		r, err := w.alice.SendMessage(tctx(t), Outgoing{To: bob, Kind: kind, Body: body, ReplyTo: replyTo})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, "bob has "+body, func() bool { _, err := w.bob.TopicOf(r.ID); return err == nil })
		return r.ID
	}

	// A: a task bob finishes (a result with status done): done by the agent.
	a := send(envelope.KindTask, "Count the boxes\nin aisle 4", "")
	res, err := w.bob.Reply(tctx(t), a, "412 boxes\nall dry")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the result arrived", func() bool { return topicOf(t, w.alice, a).Count == 2 })
	ta := topicOf(t, w.alice, a)
	if ta.State != TopicDone || ta.DoneBy != DoneByAgent || ta.Conclusion != "412 boxes" || ta.ConcludedBy != bob || ta.Pending || ta.Title != "Count the boxes" || ta.ID != a {
		t.Fatalf("finished task's topic: %+v", ta)
	}
	if tb := topicOf(t, w.bob, res.ID); tb.State != TopicDone || tb.DoneBy != DoneByAgent || tb.ConcludedBy != bob {
		t.Fatalf("bob's copy: %+v", tb)
	}

	// B: a question answered by hand carries no status: not done.
	b := send(envelope.KindQuestion, "Which dock?", "")
	if _, err := w.bob.Reply(tctx(t), b, "Dock 2"); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the answer arrived", func() bool { return topicOf(t, w.alice, b).Count == 2 })
	if tb := topicOf(t, w.alice, b); tb.State != TopicActive || tb.Pending || tb.DoneBy != "" {
		t.Fatalf("hand-answered question: %+v", tb)
	}

	// C: a question nobody answered waits: pending.
	c := send(envelope.KindQuestion, "Is the truck here?", "")
	if tc := topicOf(t, w.alice, c); tc.State != TopicActive || !tc.Pending || !tc.Waiting {
		t.Fatalf("waiting question: %+v", tc)
	}

	// Mark done, kept here: another process on alice's home reads it.
	if err := w.alice.MarkTopicDone(bob, b); err != nil {
		t.Fatal(err)
	}
	other, err := Open(w.alice.home)
	if err != nil {
		t.Fatal(err)
	}
	if tb := topicOf(t, other, b); tb.State != TopicDone || tb.DoneBy != DoneByYou {
		t.Fatalf("Mark done not kept: %+v", tb)
	}
	other.Close()
	if err := w.alice.MarkTopicDone(bob, "no-such-topic"); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("mark an unknown topic: %v", err)
	}
	if err := w.alice.MarkTopicDone(bob, res.ID); !errors.Is(err, ErrNoMessage) { // a topic is named by its earliest message only
		t.Fatalf("mark by a later message: %v", err)
	}
	// Bob writes into B: active again.
	more, err := w.bob.SendMessage(tctx(t), Outgoing{To: w.alice.Address, Kind: envelope.KindMessage, Body: "Dock 3 after noon", ReplyTo: b})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob's message arrived", func() bool { return topicOf(t, w.alice, b).Count == 3 })
	if tb := topicOf(t, w.alice, more.ID); tb.State != TopicActive || tb.DoneBy != "" || tb.ID != b {
		t.Fatalf("a new message did not reopen it: %+v", tb)
	}

	// Reopen A (done by the agent): active. A name of the person's own.
	if err := w.alice.ReopenTopic(bob, a); err != nil {
		t.Fatal(err)
	}
	if err := w.alice.RenameTopic(bob, a, "  Aisle 4\ncount  "); err != nil {
		t.Fatal(err)
	}
	ta = topicOf(t, w.alice, a)
	if ta.State != TopicActive || ta.DoneBy != "" || ta.Title != "Aisle 4 count" || !ta.Renamed || ta.AutoTitle != "Count the boxes" {
		t.Fatalf("reopened and renamed: %+v", ta)
	}
	if err := w.alice.RenameTopic(bob, a, strings.Repeat("x", TopicTitleMax+1)); !errors.Is(err, ErrTopicTitle) {
		t.Fatalf("too long a name: %v", err)
	}
	if err := w.alice.RenameTopic(bob, a, ""); err != nil {
		t.Fatal(err)
	}
	if ta = topicOf(t, w.alice, a); ta.Title != "Count the boxes" || ta.Renamed || ta.State != TopicActive {
		t.Fatalf("name given back: %+v", ta)
	}

	// Eight days later: A and B are archived, C still waits.
	testShift.Store(int64(8 * 24 * time.Hour / time.Second))
	for id, want := range map[string]string{a: TopicArchived, b: TopicArchived, c: TopicActive} {
		if got := topicOf(t, w.alice, id); got.State != want {
			t.Fatalf("after 8 days %s: %+v", id, got)
		}
	}
	threads, counts, err := w.alice.TopicOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 1 || threads[0].ID != c || len(counts) != 1 || counts[0].Total != 3 || counts[0].Archived != 2 || counts[0].Latest.ID == "" {
		t.Fatalf("overview after 8 days: %+v %+v", threads, counts)
	}
	page, err := w.alice.Topics(TopicQuery{Peer: bob, State: TopicArchived})
	if err != nil || page.Matched != 2 || len(page.Topics) != 2 || page.Topics[0].Title == "" {
		t.Fatalf("archived page: %+v %v", page, err)
	}
	// Nothing was deleted: an archived topic opens with every message.
	if conv, err := w.alice.Conversation(b, 0, 0); err != nil || len(conv.Messages) != 3 {
		t.Fatalf("archived topic's messages: %v %v", conv, err)
	}
	// A new message makes an archived topic active again.
	send(envelope.KindMessage, "One more thing", a)
	if ta := topicOf(t, w.alice, a); ta.State != TopicActive {
		t.Fatalf("a new message did not bring it back: %+v", ta)
	}
	// Reopen brings an archived one back too, until it is quiet again.
	if err := w.alice.ReopenTopic(bob, b); err != nil {
		t.Fatal(err)
	}
	if tb := topicOf(t, w.alice, b); tb.State != TopicActive {
		t.Fatalf("reopened archived topic: %+v", tb)
	}

	// Deleting a topic forgets what was set on it.
	if err := w.alice.MarkTopicDone(bob, c); err != nil {
		t.Fatal(err)
	}
	if _, err := w.alice.DeleteThread(bob, c); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := w.alice.store.db.QueryRow(`SELECT count(*) FROM topic_state WHERE topic = ?`, c).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a deleted topic's state stays: %d %v", n, err)
	}
}

// The All topics list pages most recently active first without repeats,
// even when a topic starts between pages; search reads titles (names given
// here too) and last lines, in any case, over one peer or every peer.
func TestTopicPagesAndSearch(t *testing.T) {
	w := newWorld(t, "")
	bob := w.bob.Address
	var ids []string
	for i := range 60 {
		word := "alpha"
		if i%3 == 0 {
			word = "Beta"
		}
		r, err := w.alice.SendMessage(tctx(t), Outgoing{To: bob, Kind: envelope.KindMessage, Body: fmt.Sprintf("%s report %d\nmore", word, i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	seen := map[string]bool{}
	var pages []int
	before := ""
	for {
		p, err := w.alice.Topics(TopicQuery{Peer: bob, Before: before, Limit: 25})
		if err != nil {
			t.Fatal(err)
		}
		if want := 60 + min(len(pages), 1); p.Matched != want {
			t.Fatalf("matched %d, want %d", p.Matched, want)
		}
		for i, x := range p.Topics {
			if seen[x.ID] {
				t.Fatalf("%s listed twice", x.ID)
			}
			seen[x.ID] = true
			if x.Title == "" || x.State != TopicActive || (i > 0 && newer(x, p.Topics[i-1])) {
				t.Fatalf("page entry %d: %+v", i, x)
			}
		}
		pages = append(pages, len(p.Topics))
		if len(pages) == 1 { // a topic starts between pages: it is newer than the cursor, so it is not on the later pages
			if _, err := w.alice.SendMessage(tctx(t), Outgoing{To: bob, Kind: envelope.KindMessage, Body: "late arrival"}); err != nil {
				t.Fatal(err)
			}
		}
		if p.Next == "" {
			break
		}
		before = p.Next
	}
	if fmt.Sprint(pages) != "[25 25 10]" || len(seen) != 60 {
		t.Fatalf("pages %v, %d topics", pages, len(seen))
	}
	if p, err := w.alice.Topics(TopicQuery{Peer: bob, Query: "beta"}); err != nil || p.Matched != 20 {
		t.Fatalf("search beta: %+v %v", p.Matched, err)
	}
	if p, err := w.alice.Topics(TopicQuery{Query: "BETA report 57"}); err != nil || p.Matched != 1 || p.Topics[0].Peer != bob {
		t.Fatalf("search every peer: %+v %v", p, err)
	}
	if err := w.alice.RenameTopic(bob, ids[5], "Zebra crossing"); err != nil {
		t.Fatal(err)
	}
	if p, err := w.alice.Topics(TopicQuery{Peer: bob, Query: "zebra"}); err != nil || p.Matched != 1 || p.Topics[0].Title != "Zebra crossing" || p.Topics[0].AutoTitle != "alpha report 5" {
		t.Fatalf("search a name given here: %+v %v", p, err)
	}
	if p, err := w.alice.Topics(TopicQuery{Peer: bob, Query: "alpha report 5"}); err != nil || p.Matched < 1 {
		t.Fatalf("search the automatic name of a renamed topic: %+v %v", p, err)
	}
	if err := w.alice.MarkTopicDone(bob, ids[7]); err != nil {
		t.Fatal(err)
	}
	if p, err := w.alice.Topics(TopicQuery{Peer: bob, State: TopicDone}); err != nil || p.Matched != 1 || p.Topics[0].ID != ids[7] || p.Topics[0].DoneBy != DoneByYou {
		t.Fatalf("done filter: %+v %v", p, err)
	}
	for _, bad := range []TopicQuery{{State: "deleted"}, {Limit: TopicPageMax + 1}, {Limit: -1}, {Before: "yesterday"}} {
		if _, err := w.alice.Topics(bad); !errors.Is(err, ErrTopicQuery) {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}
