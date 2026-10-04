package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// D3's notice on the page (review finding 3): Alice's own agents that
// joined without her accept are listed among the overview's review items as
// notices with reason "self_consented", their conversation and agent, never
// as decisions; resolve with the participation's id dismisses one, and the
// agent stays.
func TestLiveSelfConsentNotice(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	pa, pb := NewLive(alice), NewLive(bob)
	if _, _, err := pa.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := pb.CreatePerson("Bob"); err != nil {
		t.Fatal(err)
	}
	var conv string
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if conv, err = pa.NewDM(bob.Address); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("alice's DM with bob: %v", err)
		}
	}
	if err := alice.SetResponder(&client.Responder{Harness: "claude", Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	named, err := alice.CreateLocalAgent("Builder", client.Responder{Harness: "claude", Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := alice.PublishAgentCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	def, err := pa.InviteAgent(AgentInvite{Conv: conv, Host: alice.Address})
	if err != nil || def.State != "active" {
		t.Fatalf("own default agent: %+v %v", def, err)
	}
	builder, err := pa.InviteAgent(AgentInvite{Conv: conv, Host: alice.Address, AgentID: named.ID})
	if err != nil || builder.State != "active" {
		t.Fatalf("own named agent: %+v %v", builder, err)
	}

	// The page's JSON, as the browser reads it.
	review := func(l *Live) map[string]map[string]any {
		t.Helper()
		o, err := l.Overview()
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(o.Review)
		var items []map[string]any
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatal(err)
		}
		out := map[string]map[string]any{}
		for _, it := range items {
			out[it["id"].(string)] = it
		}
		return out
	}
	items := review(pa)
	for _, c := range []struct{ pid, agent string }{{def.PID, ""}, {builder.PID, named.ID}} {
		it, ok := items[c.pid]
		if !ok {
			t.Fatalf("no review item for %s: %v", c.pid, items)
		}
		agent, _ := it["agent_id"].(string)
		if it["reason"] != "self_consented" || it["notice"] != true || it["conv"] != conv || agent != c.agent || it["peer"] != alice.Address || it["why"] == "" {
			t.Fatalf("review item for %s: %v", c.pid, it)
		}
	}
	if len(items) != 2 {
		t.Fatalf("review items: %v", items)
	}
	if other := review(pb); len(other) != 0 {
		t.Fatalf("bob's page lists alice's notices: %v", other)
	}

	if _, err := pa.Act(Action{Do: DoResolve, ID: def.PID}); err != nil {
		t.Fatal(err)
	}
	items = review(pa)
	if _, ok := items[def.PID]; ok || len(items) != 1 {
		t.Fatalf("after dismissing %s: %v", def.PID, items)
	}
	if p, err := alice.Participation(def.PID); err != nil || p.State != client.PartActive {
		t.Fatalf("dismissing the notice ended the agent: %+v %v", p, err)
	}
	if _, err := pa.Act(Action{Do: DoResolve, ID: def.PID}); err == nil {
		t.Fatal("a notice dismissed twice")
	}

	// The other one, as the page does it: listed in GET /api/overview,
	// dismissed with POST /api/act {"do":"resolve","id":PID}.
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(pa, strings.TrimPrefix(ts.URL, "http://"), testToken)
	listed := func() []ReviewItem {
		t.Helper()
		var o Overview
		if resp := do(t, ts, "GET", "/api/overview", "", authed(ts, nil)); resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&o) != nil {
			t.Fatalf("GET /api/overview: %d", resp.StatusCode)
		}
		return o.Review
	}
	if r := listed(); len(r) != 1 || r[0].ID != builder.PID || r[0].Reason != ReasonSelfConsented || !r[0].Notice {
		t.Fatalf("the page's review items: %+v", r)
	}
	if resp := do(t, ts, "POST", "/api/act", `{"do":"resolve","id":"`+builder.PID+`"}`, post(ts)); resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/act resolve: %d", resp.StatusCode)
	}
	if r := listed(); len(r) != 0 {
		t.Fatalf("after the page dismissed it: %+v", r)
	}
	if p, err := alice.Participation(builder.PID); err != nil || p.State != client.PartActive {
		t.Fatalf("the page's dismissal ended the agent: %+v %v", p, err)
	}
}
