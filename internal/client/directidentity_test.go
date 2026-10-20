package client

import (
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
	"time"
)

// Stage admitted original requests and host replies, using the production
// device-thread/summary/query paths. No native harness runs in this fixture.
func TestConcreteAgentTopicsScopeBeforePaginationAndArchivedCounts(t *testing.T) {
	stub := installStub(t, "answer")
	w := newWorld(t, "")
	a, err := w.bob.CreateLocalAgent("Same name", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.bob.CreateLocalAgent("Same name", Responder{Harness: "stub", Dir: stub.dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range []string{a.ID, b.ID} {
		for i := 0; i < TopicPageMax+3; i++ {
			id := namedExecutorRequest(t, w, agent, envelope.KindQuestion, "")
			reply := envelope.Inner{ID: protocol.NewID(), From: w.bob.Address, To: w.alice.Address, TS: time.Now().Unix(), Kind: envelope.KindAnswer, Status: envelope.StatusDone, Body: "Done", ReplyTo: id, AgentID: agent}
			if err = w.alice.store.addInbox(reply, w.bob.Self().Fingerprint()); err != nil {
				t.Fatal(err)
			}
		}
	}
	old := time.Now().Add(-30 * 24 * time.Hour).Unix()
	if _, err = w.alice.store.db.Exec(`UPDATE outbox SET created_at=? WHERE conv IS NULL`, old); err != nil {
		t.Fatal(err)
	}
	if _, err = w.alice.store.db.Exec(`UPDATE inbox SET received_at=? WHERE conv IS NULL`, old); err != nil {
		t.Fatal(err)
	}
	_, counts, err := w.alice.TopicOverview(false)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]PeerTopics{}
	for _, c := range counts {
		if c.Peer == w.bob.Address {
			by[c.Latest.AgentID] = c
		}
	}
	for _, agent := range []string{a.ID, b.ID} {
		if c := by[agent]; c.Total != TopicPageMax+3 || c.Archived != TopicPageMax+3 || c.Latest.AgentKey != w.bob.Self().Fingerprint() {
			t.Fatalf("archived exact-agent count %+v", c)
		}
		q := TopicQuery{Peer: w.bob.Address, AgentHost: w.bob.Address, AgentKey: w.bob.Self().Fingerprint(), AgentID: agent, State: TopicArchived, Limit: 17}
		seen := map[string]bool{}
		for {
			page, e := w.alice.Topics(q)
			if e != nil {
				t.Fatal(e)
			}
			if page.Matched != TopicPageMax+3 {
				t.Fatalf("matched %d", page.Matched)
			}
			for _, topic := range page.Topics {
				if topic.AgentID != agent || topic.AgentKey != q.AgentKey || topic.AgentHost != q.AgentHost || seen[topic.ID] {
					t.Fatalf("wrong/duplicate destination %+v", topic)
				}
				seen[topic.ID] = true
			}
			if page.Next == "" {
				break
			}
			q.Before = page.Next
		}
		if len(seen) != TopicPageMax+3 {
			t.Fatal("truncated exact agent topics", len(seen))
		}
	}
	if _, err = w.alice.Topics(TopicQuery{Peer: w.bob.Address, AgentID: a.ID}); err == nil {
		t.Fatal("partial destination accepted")
	}
}
