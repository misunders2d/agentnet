package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// needsYouItem is the overview's needs-you item with reason (and, if id is
// given, that request), if listed.
func needsYouItem(o Overview, reason, id string) (ConvItem, bool) {
	for _, it := range o.NeedsYou {
		if it.Reason == reason && (id == "" || it.ID == id) {
			return it, true
		}
	}
	return ConvItem{}, false
}

// Needs-you on the host's page (ROOM_V1 §4.5): an invitation for Alice's
// agent, Bob's task to it waiting for her accept, and the agent's own
// needs-human are listed with their reason and conversation, apart from
// Bob's question for Alice herself (held, answered in the DM) and from
// device review. Opening the overview or the conversation decides nothing:
// each item leaves only by a decision through /api/dm/agent/decide or
// /api/act. The agent is a stand-in binary named like a supported harness
// (no model is called).
func TestLiveNeedsYou(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	t.Setenv("HOME", t.TempDir()) // nothing of the real harness's sessions is read
	bin := t.TempDir()
	log := filepath.Join(bin, "runs")
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\necho run >> '"+log+"'\nprintf 'AGENTNET: NEEDS-HUMAN\\nwhich key do you mean?\\n'\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	runs := func() int { data, _ := os.ReadFile(log); return strings.Count(string(data), "run") }
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, _ := alice.Invite(ctx, "bob", time.Hour, false)
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	if err := alice.SetResponder(&client.Responder{Harness: "claude", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	runDaemon(t, alice)
	runDaemon(t, bob)
	pa, pb := NewLive(alice), NewLive(bob)
	eventually := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(20 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	pa.CreatePerson("Alice")
	pb.CreatePerson("Bob")
	var conv string
	eventually("a DM", func() bool { conv, err = pb.NewDM(alice.Address); return err == nil })

	// Alice's page, through its HTTP API as the browser asks it.
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(pa, strings.TrimPrefix(ts.URL, "http://"), testToken)
	get := func(path string, v any) {
		t.Helper()
		resp := do(t, ts, "GET", path, "", authed(ts, nil))
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(v) != nil {
			t.Fatalf("GET %s: %d", path, resp.StatusCode)
		}
	}
	decide := func(path, body string) {
		t.Helper()
		if resp := do(t, ts, "POST", path, body, post(ts)); resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s %s: %d", path, body, resp.StatusCode)
		}
	}
	overview := func() Overview { var o Overview; get("/api/overview", &o); return o }
	open := func() { // what a click on an item or an alert does: it opens, never decides
		t.Helper()
		var d DMThread
		get("/api/dm?id="+conv, &d)
		overview()
	}

	// Bob's question for Alice herself: held for her, listed apart, with
	// nothing to decide (it is answered in the DM).
	q, err := bob.SendConv(ctx, conv, client.ConvOutgoing{Kind: envelope.KindQuestion, Body: "lunch at noon?"})
	if err != nil {
		t.Fatal(err)
	}
	eventually("the question held for alice", func() bool { return len(overview().Held) == 1 })
	o := overview()
	if h := o.Held[0]; h.Reason != client.ReviewHeldTurn || h.Conv != conv || h.ID != q.ID || h.Peer != bob.Address || h.Kind != envelope.KindQuestion ||
		h.Excerpt != "lunch at noon?" || len(h.Actions) != 0 || h.DecideOn != "" || !strings.Contains(h.Why, "nothing runs it") {
		t.Fatalf("the held question: %+v", h)
	}
	if len(o.NeedsYou) != 0 || len(o.Review) != 0 {
		t.Fatalf("a held person turn is a decision: needs-you %+v review %+v", o.NeedsYou, o.Review)
	}

	// Bob invites Alice's agent: it waits for her decision, and opening it
	// decides nothing.
	inv, err := pb.InviteAgent(AgentInvite{Conv: conv, Host: alice.Address, Note: "help with keys"})
	if err != nil {
		t.Fatal(err)
	}
	eventually("the invitation in needs-you", func() bool { _, ok := needsYouItem(overview(), client.ReviewInvite, ""); return ok })
	it, _ := needsYouItem(overview(), client.ReviewInvite, "")
	if it.Conv != conv || it.PID != inv.PID || it.ID != "" || it.Peer != bob.Address || it.Excerpt != "help with keys" ||
		strings.Join(it.Actions, ",") != DoAccept+","+DoDecline || it.DecideOn != "" || !strings.Contains(it.Why, "Nothing runs unless you accept") {
		t.Fatalf("the invitation: %+v", it)
	}
	if ob, err := pb.Overview(); err != nil || len(ob.NeedsYou) != 0 {
		t.Fatalf("the inviter's page lists the host's decision: %+v %v", ob.NeedsYou, err)
	}
	open()
	if info, err := alice.Participation(inv.PID); err != nil || info.State != client.PartInvited {
		t.Fatalf("opening decided the invitation: %+v %v", info, err)
	}
	decide("/api/dm/agent/decide", `{"pid":"`+inv.PID+`","accept":true}`)
	if _, ok := needsYouItem(overview(), client.ReviewInvite, ""); ok {
		t.Fatal("an accepted invitation still waits")
	}
	eventually("active at bob", func() bool { d, _ := pb.DM(conv); return len(d.Agents) == 1 && d.Agents[0].CanAsk })

	// Bob's task waits for Alice's one-time accept; opening it runs nothing.
	sent, err := pb.AskAgent(AgentAsk{PID: inv.PID, Kind: "task", Body: "rotate the key"})
	if err != nil {
		t.Fatal(err)
	}
	eventually("the task in needs-you", func() bool { _, ok := needsYouItem(overview(), client.ReviewAwaiting, sent.ID); return ok })
	it, _ = needsYouItem(overview(), client.ReviewAwaiting, sent.ID)
	if it.Conv != conv || it.PID != inv.PID || it.Peer != bob.Address || it.Kind != envelope.KindTask || it.Excerpt != "rotate the key" ||
		strings.Join(it.Actions, ",") != DoAccept+","+DoDecline || it.Why == "" {
		t.Fatalf("the waiting task: %+v", it)
	}
	if ob, err := pb.Overview(); err != nil || len(ob.NeedsYou) != 0 || len(ob.Held) != 0 {
		t.Fatalf("the asker's page lists the host's decision: %+v %+v %v", ob.NeedsYou, ob.Held, err)
	}
	open()
	if m := goMessage(alice, conv, "rotate the key"); m.State != "awaiting" || runs() != 0 {
		t.Fatalf("opening decided the task: state %q, %d run(s)", m.State, runs())
	}

	// Accepted through /api/act it runs once; the agent says Alice must
	// decide, which waits for her in turn until resolved through /api/act.
	decide("/api/act", `{"do":"accept","id":"`+sent.ID+`"}`)
	eventually("the agent's needs-human", func() bool { _, ok := needsYouItem(overview(), client.ReviewNeedsHuman, sent.ID); return ok })
	o = overview()
	it, _ = needsYouItem(o, client.ReviewNeedsHuman, sent.ID)
	if _, ok := needsYouItem(o, client.ReviewAwaiting, sent.ID); ok || it.Conv != conv || it.PID != inv.PID ||
		strings.Join(it.Actions, ",") != DoAccept+","+DoResolve || !strings.Contains(it.Why, "which key do you mean?") {
		t.Fatalf("needs-human: %+v (all %+v)", it, o.NeedsYou)
	}
	open()
	if m := goMessage(alice, conv, "rotate the key"); m.State != "needs_human" || runs() != 1 {
		t.Fatalf("opening decided the needs-human: state %q, %d run(s)", m.State, runs())
	}
	decide("/api/act", `{"do":"resolve","id":"`+sent.ID+`"}`)
	o = overview()
	if len(o.NeedsYou) != 0 || len(o.Held) != 1 || o.Held[0].ID != q.ID {
		t.Fatalf("after resolving: needs-you %+v held %+v", o.NeedsYou, o.Held)
	}
	if m := goMessage(alice, conv, "rotate the key"); m.State != "resolved" || runs() != 1 {
		t.Fatalf("resolved: state %q, %d run(s)", m.State, runs())
	}

	// Declined through /api/act instead: it leaves needs-you and never
	// runs; Bob's page shows the request declined by Alice's device (its
	// status), with no reply in the DM: the reason stays with Alice.
	other, err := pb.AskAgent(AgentAsk{PID: inv.PID, Kind: "task", Body: "drop the table"})
	if err != nil {
		t.Fatal(err)
	}
	eventually("the second task in needs-you", func() bool { _, ok := needsYouItem(overview(), client.ReviewAwaiting, other.ID); return ok })
	var d DMThread
	get("/api/dm?id="+conv, &d)
	for _, m := range d.Messages {
		if m.ID == other.ID && strings.Join(m.Actions, ",") != DoAccept+","+DoDecline {
			t.Fatalf("the waiting task's actions in the DM: %+v", m)
		}
	}
	decide("/api/act", `{"do":"decline","id":"`+other.ID+`","reason":"not that one"}`)
	if _, ok := needsYouItem(overview(), client.ReviewAwaiting, other.ID); ok {
		t.Fatal("a declined task still waits")
	}
	if m := goMessage(alice, conv, "drop the table"); m.State != "declined" || m.Detail != "not that one" || runs() != 1 {
		t.Fatalf("declined: state %q (%q), %d run(s)", m.State, m.Detail, runs())
	}
	eventually("bob's page to show it declined", func() bool {
		d, err := pb.DM(conv)
		if err != nil {
			return false
		}
		for _, m := range d.Messages {
			if m.ID == other.ID {
				return m.Exec != nil && m.Exec.State == envelope.StatusDeclined && m.Exec.Host == alice.Address
			}
		}
		return false
	})
	d, err = pb.DM(conv)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range d.Messages {
		if m.ReplyTo == other.ID {
			t.Fatalf("a reply to the declined task in the DM: %+v", m)
		}
	}
	if runs() != 1 {
		t.Fatalf("a declined task ran: %d run(s)", runs())
	}
}

// An agent's invitation time in a conversation's view is the inviter's
// claim: one no page could show (past year 9999, or not after 1970) is left
// out, so the conversation still encodes; a plausible one is shown.
func TestAgentViewInvitedClaim(t *testing.T) {
	for _, c := range []struct {
		claim int64
		shown bool
	}{{1e12, false}, {253402300799, false}, {-5, false}, {0, false}, {1759500000, true}} {
		v := agentView(client.ParticipationInfo{PID: "p", State: client.PartInvited, Invited: c.claim}, dmPeople{}, nil, false)
		if _, err := json.Marshal(v); err != nil {
			t.Fatalf("claim %d: the view no longer encodes: %v", c.claim, err)
		}
		if shown := !v.Invited.IsZero(); shown != c.shown || shown && v.Invited.Unix() != c.claim {
			t.Fatalf("claim %d: invited %v", c.claim, v.Invited)
		}
	}
}
