package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
)

// Deleting a device chat removes the whole chat: one delete leaves no row
// behind, archived topics included (owner, 2026-11-27: "delete removes the
// whole chat"; a topic is archived, not deleted). The 2026-11-27 report: a
// row of three one-message topics came back twice after "Delete topic".
func TestDeleteDeviceChatRemovesEveryTopic(t *testing.T) {
	alice, bob, live := liveWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, text := range []string{"topic one", "topic two", "topic three", "topic four"} {
		if _, err := alice.SendMessage(ctx, client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: text}); err != nil {
			t.Fatal(err)
		}
	}
	rows := func() (n int, latest string) {
		o, err := live.TopicOverview()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range o.Topics {
			if c.Peer == alice.Address && c.Latest.AgentID == "" && !c.Latest.AgentLegacy {
				n, latest = c.Total, c.Latest.ID
			}
		}
		return n, latest
	}
	var latest string
	waitFor(t, "a chat row with four topics", func() bool { n, id := rows(); latest = id; return n == 4 })
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	ts.Start()
	t.Cleanup(ts.Close)
	s = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	// Delete topic stays what it was: that one topic only.
	body, _ := json.Marshal(map[string]any{"peer": alice.Address, "thread": latest})
	if code := postJSON(t, ts, "/api/conversation/delete", string(body), nil); code != 200 {
		t.Fatalf("delete topic: %d", code)
	}
	if n, id := rows(); n != 3 {
		t.Fatalf("delete topic left %d topics", n)
	} else {
		latest = id
	}
	body, _ = json.Marshal(map[string]any{"peer": alice.Address, "thread": latest, "chat": true})
	var out struct {
		Note string `json:"note"`
	}
	if code := postJSON(t, ts, "/api/conversation/delete", string(body), &out); code != 200 || out.Note == "" {
		t.Fatalf("delete chat: %d %+v", code, out)
	}
	if n, id := rows(); n != 0 {
		t.Fatalf("the chat row came back with %d topic(s), latest %s", n, id)
	}
	// The other person keeps every topic.
	if ts, err := alice.Threads(); err != nil || len(ts) != 4 {
		t.Fatalf("the peer's copy: %d %v", len(ts), err)
	}
}
