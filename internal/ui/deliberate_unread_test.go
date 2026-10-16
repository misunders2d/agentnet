package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestComicDeliberateUnreadStore(t *testing.T) {
	runNodeCheck(t, "testdata/deliberate_unread_store_check.mjs")
}

// Exercise the real local action decoder, native attention register and both
// message projections. This is local provider evidence, not cross-device sync.
func TestLiveDeliberateUnreadHTTP(t *testing.T) {
	alice, bob, _, conv, _, _ := liveGuestWorld(t)
	live := NewLive(alice)
	sent, err := live.SendDM(DMDraft{Conv: conv, Body: "sent message needing attention"})
	if err != nil {
		t.Fatal(err)
	}
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	server = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	act := func(a Action) {
		t.Helper()
		body, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if r := do(t, ts, "POST", "/api/act", string(body), post(ts)); r.StatusCode != http.StatusOK {
			t.Fatalf("action %s: HTTP %d", a.Do, r.StatusCode)
		}
	}
	readDM := func() DMMessage {
		t.Helper()
		r := do(t, ts, "GET", "/api/dm?id="+conv, "", authed(ts, nil))
		var dm DMThread
		if r.StatusCode != http.StatusOK {
			t.Fatalf("DM HTTP %d", r.StatusCode)
		}
		if err := json.NewDecoder(r.Body).Decode(&dm); err != nil {
			t.Fatal(err)
		}
		for _, m := range dm.Messages {
			if m.ID == sent.ID {
				return m
			}
		}
		t.Fatal("sent row missing from the HTTP projection")
		return DMMessage{}
	}
	if m := readDM(); m.Dir != "out" || m.Unread || m.Attention != nil {
		t.Fatalf("initial sent projection: %+v", m)
	}
	act(Action{Do: DoUnread, ID: sent.ID, IDs: []string{sent.ID}})
	m := readDM()
	if !m.Unread || m.Attention == nil || m.Attention.Rev < 1 || m.Attention.Writer == "" {
		t.Fatalf("marked projection: %+v", m)
	}
	o, err := live.Overview()
	if err != nil {
		t.Fatal(err)
	}
	count := -1
	for _, d := range o.DMs {
		if d.ID == conv {
			count = d.Unread
		}
	}
	if count != 1 {
		t.Fatalf("effective sent-row badge: %d", count)
	}
	// Neither an old client nor an equal-revision other writer may clear it.
	act(Action{Do: DoRead, IDs: []string{sent.ID}})
	wrong := *m.Attention
	wrong.Writer = "11111111-11111111-11111111-11111111"
	if wrong.Writer == m.Attention.Writer {
		wrong.Writer = "22222222-22222222-22222222-22222222"
	}
	act(Action{Do: DoRead, IDs: []string{sent.ID}, Attention: map[string]protocol.AttentionGeneration{sent.ID: wrong}})
	if got := readDM(); !got.Unread || got.Attention == nil || *got.Attention != *m.Attention {
		t.Fatalf("stale read cleared the mark: %+v", got)
	}
	act(Action{Do: DoRead, IDs: []string{sent.ID}, Attention: map[string]protocol.AttentionGeneration{sent.ID: *m.Attention}})
	if got := readDM(); got.Unread || got.Attention != nil {
		t.Fatalf("matching read did not clear: %+v", got)
	}
	// The device-thread Message DTO carries the same effective fields.
	direct, err := alice.SendMessage(context.Background(), client.Outgoing{To: bob.Address, Kind: envelope.KindMessage, Body: "direct attention"})
	if err != nil {
		t.Fatal(err)
	}
	act(Action{Do: DoUnread, ID: direct.ID, IDs: []string{direct.ID}})
	r := do(t, ts, "GET", "/api/thread?id="+direct.ID, "", authed(ts, nil))
	var thread Thread
	if r.StatusCode != http.StatusOK {
		t.Fatalf("thread HTTP %d", r.StatusCode)
	}
	if err := json.NewDecoder(r.Body).Decode(&thread); err != nil {
		t.Fatal(err)
	}
	if len(thread.Messages) != 1 || !thread.Messages[0].Unread || thread.Messages[0].Attention == nil {
		t.Fatalf("direct effective projection: %+v", thread.Messages)
	}
}
