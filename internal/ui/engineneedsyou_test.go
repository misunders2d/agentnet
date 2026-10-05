package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// Needs-you in the browser (ROOM_V1 §4.5, §8): a browser runs no agent, so
// what waits for its person on another device of theirs is shown
// read-only, naming that device (decide_on), with no actions: an
// invitation for the laptop's agent until the laptop decides it, and the
// browser's own request that the laptop's agent left for a person (as the
// laptop reported it) until it is resolved there. A question for the
// person is held apart; an invitation for someone else's agent is not
// listed; deciding in the browser is refused. The agent is a stand-in
// binary named like a supported harness (no model is called).
func TestBrowserEngineNeedsYouReadOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stand-in")
	}
	t.Setenv("AGENTNET_NOTIFY", "off")
	t.Setenv("HOME", t.TempDir()) // nothing of the real harness's sessions is read
	bin := t.TempDir()
	agentText := "Which branch should I use?\n\nChoose the release branch before I continue.\n" + strings.Repeat("LongLine", 80)
	os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\ncat > /dev/null\nprintf 'AGENTNET: NEEDS-HUMAN\\n%s\\n' '"+agentText+"'\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	// A short session grace: the tablet's session from before its link (it
	// published no capabilities) stops counting soon after it reconnects as
	// a member, as it does after the default 30s, longer than these waits.
	relay := testhub.StartConfig(t, hub.Config{DataDir: dir, SessionGrace: time.Second}, "127.0.0.1:0")
	base := "https://" + relay.Addr
	laptop := personAgent(t, ctx, testhub.BootstrapCode(t, dir), "laptop", "Alice")
	if err := laptop.SetResponder(&client.Responder{Harness: "claude", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	code, err := laptop.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob := personAgent(t, ctx, code, "desk", "Bob")
	var conv string
	waitFor(t, "a DM with Bob", func() bool { conv, err = laptop.CreateDM(ctx, bob.Address); return err == nil })
	if _, err := laptop.SendConv(ctx, conv, client.ConvOutgoing{Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Bob has the DM", goHas(bob, conv, "hello", nil))

	// The browser joins as Alice's tablet with the laptop's link.
	var offer client.DeviceLinkOffer
	waitFor(t, "a device link", func() bool { offer, err = laptop.NewDeviceLink(ctx); return err == nil })
	o, err := protocol.DecodeLinkOffer(offer.Code)
	if err != nil {
		t.Fatal(err)
	}
	o.Invite = browserCode(t, o.Invite)
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	tablet := w.ok(map[string]any{"op": "joinLink", "code": base + "/#" + o.Encode(), "name": "tablet"})["address"].(string)
	w.ok(map[string]any{"op": "start"})
	var req client.LinkRequest
	waitFor(t, "the tablet's request", func() bool {
		rs, _ := laptop.PendingLinks()
		for _, r := range rs {
			if r.State == "pending" && r.Address == tablet {
				req = r
			}
		}
		return req.ID != ""
	})
	if err := laptop.DecideLink(ctx, req.ID, true); err != nil {
		t.Fatal(err)
	}
	w.until("linked as Alice", func() bool {
		p, _ := w.api("/api/overview", nil)["person"].(map[string]any)
		return w.ok(map[string]any{"op": "status"})["link"] == "linked" && p != nil && p["label"] == "Alice"
	})
	// Before anything is asked: the tablet has the laptop's history, and Bob
	// and the laptop each reach it directly. Until its session from before
	// the link ends, a copy for it is kept waiting (not every session of it
	// reads conversations); the session's end lets it go, and later ones go
	// directly.
	w.until("the laptop's history", func() bool { return dmMessage(w, conv, "hello") != nil })
	for _, from := range []*client.Agent{bob, laptop} {
		reachBrowser(t, w, from, conv, tablet)
	}
	list := func(name string) []any { v, _ := w.api("/api/overview", nil)[name].([]any); return v }
	item := func(reason string) map[string]any {
		for _, x := range list("needs_you") {
			if it := x.(map[string]any); it["reason"] == reason {
				return it
			}
		}
		return nil
	}

	// Bob's question for Alice herself: held apart, nothing to decide.
	if _, err := bob.SendConv(ctx, conv, client.ConvOutgoing{Kind: envelope.KindQuestion, Body: "lunch at noon?"}); err != nil {
		t.Fatal(err)
	}
	w.until("the held question", func() bool { return len(list("held")) == 1 })
	if h := list("held")[0].(map[string]any); h["reason"] != client.ReviewHeldTurn || h["conv"] != conv || h["peer"] != bob.Address ||
		h["excerpt"] != "lunch at noon?" || h["actions"] != nil || h["decide_on"] != nil || len(list("needs_you")) != 0 {
		t.Fatalf("the held question: %v (needs-you %v)", h, list("needs_you"))
	}

	// Bob invites the laptop's agent: shown here, decided there.
	inv, err := bob.InviteAgent(ctx, conv, laptop.Address, nil, nil, "help")
	if err != nil {
		t.Fatal(err)
	}
	w.until("the invitation in needs-you", func() bool { return item(client.ReviewInvite) != nil })
	it := item(client.ReviewInvite)
	if it["conv"] != conv || it["pid"] != inv.PID || it["id"] != nil || it["peer"] != bob.Address || it["excerpt"] != "help" ||
		it["decide_on"] != laptop.Address || it["actions"] != nil || !strings.Contains(it["why"].(string), "runs no agent") {
		t.Fatalf("the invitation in the browser: %v", it)
	}
	w.refuses("deciding in the browser", w.call(map[string]any{"op": "api", "path": "/api/dm/agent/decide", "body": map[string]any{"pid": inv.PID, "accept": true}}), "runs no agent")
	w.api("/api/dm?id="+conv, nil) // opening it decides nothing
	if info, err := laptop.Participation(inv.PID); err != nil || info.State != client.PartInvited {
		t.Fatalf("the invitation was decided from the browser: %+v %v", info, err)
	}
	if _, err := laptop.AcceptParticipation(ctx, inv.PID); err != nil {
		t.Fatal(err)
	}
	w.until("decided on the laptop", func() bool { return item(client.ReviewInvite) == nil })

	// The laptop invites Bob's agent: not Alice's to decide, not listed.
	theirs, err := laptop.InviteAgent(ctx, conv, bob.Address, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	agent := func(pid string) map[string]any {
		for _, x := range w.api("/api/dm?id="+conv, nil)["agents"].([]any) {
			if a := x.(map[string]any); a["pid"] == pid {
				return a
			}
		}
		return nil
	}
	w.until("Bob's agent invited, in the browser", func() bool { a := agent(theirs.PID); return a != nil && a["state"] == "invited" })
	if len(list("needs_you")) != 0 {
		t.Fatalf("an invitation for Bob's agent waits for Alice: %v", list("needs_you"))
	}

	// The tablet asks the laptop's agent, which leaves it for Alice: shown
	// here read-only as the laptop reports it, until resolved there.
	w.until("the laptop's agent active in the browser", func() bool { a := agent(inv.PID); return a != nil && a["can_ask"] == true })
	asked := w.api("/api/dm/agent/ask", map[string]any{"pid": inv.PID, "body": "deploy now?"})["id"].(string)
	w.until("the laptop's full private needs-human text", func() bool { it := item(client.ReviewNeedsHuman); return it != nil && it["why"] == agentText })
	it = item(client.ReviewNeedsHuman)
	if it["conv"] != conv || it["pid"] != inv.PID || it["id"] != asked || it["kind"] != envelope.KindQuestion || it["excerpt"] != "deploy now?" ||
		it["decide_on"] != laptop.Address || it["actions"] != nil || it["why"] != agentText {
		t.Fatalf("needs-human in the browser: %v", it)
	}
	phoneTurn := dmMessage(w, conv, "deploy now?")
	if phoneTurn == nil || phoneTurn["job_detail"] != agentText || phoneTurn["actions"] != nil {
		t.Fatalf("phone lost full agent turn or gained actions: %v", phoneTurn)
	}
	w.refuses("accepting in the browser", w.call(map[string]any{"op": "api", "path": "/api/act", "body": map[string]any{"do": "accept", "id": asked}}), "Nothing runs in this browser")
	m := goMessage(laptop, conv, "deploy now?")
	if m.State != "needs_human" {
		t.Fatalf("on the laptop: %+v", m)
	}
	if err := laptop.Resolve(m.ID); err != nil {
		t.Fatal(err)
	}
	w.until("resolved on the laptop", func() bool { return item(client.ReviewNeedsHuman) == nil })
	if len(list("held")) != 1 {
		t.Fatalf("the held question went with the decisions: %v", list("held"))
	}
}

// reachBrowser sends plain messages from a into conv until its copy to the
// browser device is not kept waiting, then until the browser holds that
// message: what a sends next goes to it directly.
func reachBrowser(t *testing.T, w *engineNode, a *client.Agent, conv, browser string) {
	t.Helper()
	var body string
	for i, deadline := 0, time.Now().Add(20*time.Second); ; i++ {
		if time.Now().After(deadline) {
			t.Fatalf("%s never reached %s directly", a.Address, browser)
		}
		body = "reach " + browser + " " + strings.Repeat(".", i)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		sent, err := a.SendConv(ctx, conv, client.ConvOutgoing{Body: body})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.IndexFunc(sent.Copies, func(c client.ConvCopy) bool { return c.To == browser }); i >= 0 && sent.Copies[i].State != "waiting" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	w.until(browser+" to hold "+body, func() bool { return dmMessage(w, conv, body) != nil })
}

// An invitation's claimed time never breaks the browser's overview or a
// conversation's agents (testdata/needsyou_engine_check.mjs).
func TestBrowserNeedsYouClaimedTimes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/needsyou_engine_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}
