package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// Topics through the page's routes on a real installation: the overview
// carries each peer's counts and says the list is served; GET /api/topics
// pages and filters; POST /api/topic/{rename,done,reopen} passes the same
// guard as every other change (same origin, JSON, cookie) and is kept here;
// an open thread names its topic. The demo provider has no topic list.
func TestLiveTopicRoutes(t *testing.T) {
	alice, bob, live := liveWorld(t)
	var ids []string
	for _, body := range []string{"Order tape\nplease", "Which dock?", "Truck at 3"} {
		r, err := alice.SendMessage(t.Context(), client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: body})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	read := func(path string, v any) int {
		t.Helper()
		r := do(t, ts, "GET", path, "", authed(ts, nil))
		if r.StatusCode == 200 && v != nil {
			if err := json.NewDecoder(r.Body).Decode(v); err != nil {
				t.Fatal(err)
			}
		}
		return r.StatusCode
	}
	var o Overview
	deadline := time.Now().Add(15 * time.Second)
	for {
		if read("/api/overview", &o) != 200 {
			t.Fatal("overview")
		}
		if len(o.Topics) == 1 && o.Topics[0].Total == 3 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !o.TopicList || len(o.Topics) != 1 || o.Topics[0].Peer != alice.Address || o.Topics[0].Total != 3 || o.Topics[0].Archived != 0 || !strings.Contains(strings.Join(ids, ","), o.Topics[0].Latest.ID) {
		t.Fatalf("overview topics: %v %+v", o.TopicList, o.Topics)
	}
	for _, th := range o.Threads {
		if th.State != TopicActive {
			t.Fatalf("thread state: %+v", th)
		}
	}
	// The messenger's overview (topics=1) leaves archived topics out and
	// carries the same counts; nothing is archived yet, so it lists the same.
	var compact Overview
	if read("/api/overview?topics=1", &compact) != 200 || !compact.TopicList || len(compact.Topics) != 1 || compact.Topics[0].Total != 3 || len(compact.Threads) != len(o.Threads) {
		t.Fatalf("overview?topics=1: %+v", compact.Topics)
	}
	var page TopicPage
	if read("/api/topics?"+url.Values{"peer": {alice.Address}, "limit": {"2"}}.Encode(), &page) != 200 || len(page.Topics) != 2 || page.Next == "" || page.Matched != 3 {
		t.Fatalf("first page: %+v", page)
	}
	listed, before := map[string]bool{page.Topics[0].ID: true, page.Topics[1].ID: true}, page.Next
	page = TopicPage{}
	if read("/api/topics?"+url.Values{"peer": {alice.Address}, "limit": {"2"}, "before": {before}}.Encode(), &page) != 200 || len(page.Topics) != 1 || page.Next != "" || listed[page.Topics[0].ID] {
		t.Fatalf("second page: %+v", page)
	}
	if read("/api/topics?q=DOCK", &page) != 200 || page.Matched != 1 || page.Topics[0].Title != "Which dock?" {
		t.Fatalf("search: %+v", page)
	}
	for _, bad := range []string{"state=gone", "limit=x", "limit=1000", "before=nope"} {
		if code := read("/api/topics?"+bad, nil); code != http.StatusConflict {
			t.Errorf("%s: %d", bad, code)
		}
	}

	change := func(what string, c TopicChange, hdr map[string]string) (int, string) {
		t.Helper()
		b, _ := json.Marshal(c)
		r := do(t, ts, "POST", "/api/topic/"+what, string(b), hdr)
		body, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(body)
	}
	if code, _ := change("done", TopicChange{Peer: alice.Address, ID: ids[0]}, authed(ts, map[string]string{"Content-Type": "application/json"})); code != http.StatusForbidden {
		t.Fatalf("a change without its origin: %d", code)
	}
	if code, _ := change("done", TopicChange{Peer: alice.Address, ID: ids[0]}, authed(ts, map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json"})); code != http.StatusForbidden {
		t.Fatalf("a cross-origin change: %d", code)
	}
	if r := do(t, ts, "POST", "/api/topic/done", `{"peer":"`+alice.Address+`","id":"`+ids[0]+`","sudo":true}`, post(ts)); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown field: %d", r.StatusCode)
	}
	if code, body := change("done", TopicChange{Peer: alice.Address, ID: ids[0]}, post(ts)); code != 200 || !strings.Contains(body, "on this device") {
		t.Fatalf("mark done: %d %s", code, body)
	}
	if code, body := change("rename", TopicChange{Peer: alice.Address, ID: ids[1], Title: "Dock question"}, post(ts)); code != 200 || !strings.Contains(body, "syncs across your linked devices") {
		t.Fatalf("rename: %d %s", code, body)
	}
	if code, _ := change("rename", TopicChange{Peer: alice.Address, ID: ids[1], Title: strings.Repeat("y", client.TopicTitleMax+1)}, post(ts)); code != http.StatusConflict {
		t.Fatalf("too long a name: %d", code)
	}
	if code, _ := change("done", TopicChange{Peer: alice.Address, ID: "0123"}, post(ts)); code != http.StatusNotFound {
		t.Fatalf("an unknown topic: %d", code)
	}
	if code, _ := change("bogus", TopicChange{Peer: alice.Address, ID: ids[0]}, post(ts)); code != http.StatusNotFound {
		t.Fatalf("an unknown change: %d", code)
	}
	var th Thread
	if read("/api/thread?id="+ids[0], &th) != 200 || th.Topic == nil || th.Topic.State != TopicDone || th.Topic.DoneBy != client.DoneByYou {
		t.Fatalf("thread topic after Mark done: %+v", th.Topic)
	}
	if read("/api/thread?id="+ids[1], &th) != 200 || th.Topic.Title != "Dock question" || !th.Topic.Renamed || th.Topic.AutoTitle != "Which dock?" {
		t.Fatalf("thread topic after rename: %+v", th.Topic)
	}
	topic := func(id string) *ThreadSummary { // a fresh decode: omitted fields must not keep an earlier read's values
		t.Helper()
		var th Thread
		if read("/api/thread?id="+id, &th) != 200 || th.Topic == nil {
			t.Fatalf("thread %s has no topic", id)
		}
		return th.Topic
	}
	// A blank name gives back the automatic one, and says so.
	if code, body := change("rename", TopicChange{Peer: alice.Address, ID: ids[1], Title: " \n "}, post(ts)); code != 200 || !strings.Contains(body, "first message again") {
		t.Fatalf("blank rename: %d %s", code, body)
	}
	if tp := topic(ids[1]); tp.Title != "Which dock?" || tp.Renamed {
		t.Fatalf("thread topic after a blank rename: %+v", tp)
	}
	// Mark done covers only the messages the page showed (Count): one that
	// came after keeps the topic active, and the note says why.
	if _, err := alice.SendMessage(t.Context(), client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: "Truck is late", ReplyTo: ids[2]}); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if topic(ids[2]).Count == 2 || time.Now().After(deadline) {
			break
		}
	}
	if code, body := change("done", TopicChange{Peer: alice.Address, ID: ids[2], Count: 1}, post(ts)); code != 200 || !strings.Contains(body, "newer message") {
		t.Fatalf("mark done with a message unseen: %d %s", code, body)
	}
	if tp := topic(ids[2]); tp.State != TopicActive {
		t.Fatalf("a mark covered an unseen message: %+v", tp)
	}
	if code, body := change("done", TopicChange{Peer: alice.Address, ID: ids[2], Count: 2}, post(ts)); code != 200 || strings.Contains(body, "newer message") {
		t.Fatalf("mark done with every message seen: %d %s", code, body)
	}
	if tp := topic(ids[2]); tp.State != TopicDone {
		t.Fatalf("mark done with every message seen: %+v", tp)
	}
	if code, _ := change("reopen", TopicChange{Peer: alice.Address, ID: ids[0]}, post(ts)); code != 200 {
		t.Fatalf("reopen: %d", code)
	}
	if read("/api/topics?state=done", &page) != 200 || page.Matched != 1 || page.Topics[0].ID != ids[2] {
		t.Fatalf("done after reopen: %+v", page)
	}

	demo, _ := newTestServer(t)
	if r := do(t, demo, "GET", "/api/topics", "", authed(demo, nil)); r.StatusCode != http.StatusNotFound {
		t.Fatalf("demo topics: %d", r.StatusCode)
	}
	var d Overview
	r := do(t, demo, "GET", "/api/overview", "", authed(demo, nil))
	if err := json.NewDecoder(r.Body).Decode(&d); err != nil || d.TopicList {
		t.Fatalf("demo says it lists topics: %v", err)
	}
}
