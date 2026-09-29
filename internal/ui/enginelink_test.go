package ui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// One person on several devices, with the browser device as either side
// of a link (client/link.go, history.go): the new device, linked by a Go
// device and given its chats; and the approving device, linking a Go
// device and giving it its chats.

// personAgent joins a Go device with code and sets up its person.
func personAgent(t *testing.T, ctx context.Context, code, name, label string) *client.Agent {
	t.Helper()
	a, err := client.Join(ctx, filepath.Join(t.TempDir(), name), code, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	runDaemon(t, a)
	if _, err := a.CreatePerson(ctx, label); err != nil {
		t.Fatal(err)
	}
	return a
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); !cond(); time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// goHas reports whether a holds a message of conv with body (and, if
// check is given, one it accepts).
func goHas(a *client.Agent, conv, body string, check func(client.ConvMessage) bool) func() bool {
	return func() bool {
		ms, _ := a.ConversationMessages(conv)
		for _, m := range ms {
			if m.Body == body && (check == nil || check(m)) {
				return true
			}
		}
		return false
	}
}

// dmMessage is the message with body in the browser's view of conv, or nil.
func dmMessage(w *engineNode, conv, body string) map[string]any {
	v := w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + conv})
	if v["error"] != nil {
		return nil
	}
	for _, m := range v["v"].(map[string]any)["messages"].([]any) {
		if m := m.(map[string]any); m["body"] == body {
			return m
		}
	}
	return nil
}

func deviceCount(o map[string]any) int {
	p, _ := o["person"].(map[string]any)
	if p == nil {
		return 0
	}
	ds, _ := p["devices"].([]any)
	return len(ds)
}

// The browser joins as a new device of a person with a Go device's link:
// it waits for approval, doing nothing else; once approved it is that
// person, has the chats from before as history (sent ones as yours),
// receives new messages and sends to both the other person and the Go
// device.
func TestBrowserEngineLinksAsNewDevice(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + hub.Addr
	laptop := personAgent(t, ctx, testhub.BootstrapCode(t, dir), "laptop", "Alice")
	code, err := laptop.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob := personAgent(t, ctx, code, "desk", "Bob")
	var conv string
	waitFor(t, "a DM with Bob", func() bool { conv, err = laptop.CreateDM(ctx, bob.Address); return err == nil })
	if _, err := laptop.SendConv(ctx, conv, client.ConvOutgoing{Body: "before the tablet"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Bob has it", goHas(bob, conv, "before the tablet", nil))
	if _, err := bob.SendConv(ctx, conv, client.ConvOutgoing{Body: "bob before"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the laptop has Bob's", goHas(laptop, conv, "bob before", nil))

	// The laptop's link, as its QR opens this server's page (the invite in
	// it for a browser: this test Hub's certificate is trusted by node).
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
	ov := w.api("/api/overview", nil)
	if ov["role"] != "unset" || ov["link"].(map[string]any)["state"] != "pending" || ov["person"] != nil {
		t.Fatalf("pending: role %v link %v person %v", ov["role"], ov["link"], ov["person"])
	}
	if v := w.call(map[string]any{"op": "api", "path": "/api/person", "body": map[string]any{"label": "Mallory"}}); v["error"] == nil {
		t.Fatal("a device waiting for approval created a person of its own")
	}
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
	w.until("linked as Alice on two devices", func() bool {
		o := w.api("/api/overview", nil)
		p, _ := o["person"].(map[string]any)
		return w.ok(map[string]any{"op": "status"})["link"] == "linked" && p != nil && p["label"] == "Alice" && deviceCount(o) == 2 && o["link"] == nil
	})

	// The chats from before, as history from the laptop: the laptop's own
	// message is yours, Bob's is his.
	w.until("the history", func() bool { return dmMessage(w, conv, "before the tablet") != nil && dmMessage(w, conv, "bob before") != nil })
	mine, his := dmMessage(w, conv, "before the tablet"), dmMessage(w, conv, "bob before")
	if mine["dir"] != "out" || his["dir"] != "in" || mine["synced_from"] != laptop.Address || his["synced_from"] != laptop.Address || his["unread"] == true {
		t.Fatalf("history: %v / %v", mine, his)
	}

	// After: Bob's new message reaches the tablet (directly, or forwarded
	// by the laptop when Bob sent to the roster before it).
	if _, err := bob.SendConv(ctx, conv, client.ConvOutgoing{Body: "bob after"}); err != nil {
		t.Fatal(err)
	}
	w.until("Bob's new message", func() bool { return dmMessage(w, conv, "bob after") != nil })
	// The tablet sends: a copy to Bob, and one to the laptop, kept as yours there.
	sent := w.api("/api/dm/send", map[string]any{"conv": conv, "body": "from the tablet"})
	if len(sent["copies"].([]any)) != 2 {
		t.Fatalf("copies: %v", sent)
	}
	waitFor(t, "Bob has the tablet's", goHas(bob, conv, "from the tablet", func(m client.ConvMessage) bool { return m.Dir == "in" }))
	waitFor(t, "the laptop has the tablet's as its own", goHas(laptop, conv, "from the tablet", func(m client.ConvMessage) bool { return m.Via == tablet }))
}

// The browser approves a Go device of its person: its link, the request
// shown to the person, the approval; the Go device then has the browser's
// chats as history and new ones directly; the browser removes it again.
func TestBrowserEngineApprovesNewDevice(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + hub.Addr
	laptop := personAgent(t, ctx, testhub.BootstrapCode(t, dir), "laptop", "Alice")
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	code, _ := laptop.Invite(ctx, "dana", time.Hour, false)
	phone := w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})["address"].(string)
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	w.until("connected with members", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})
	var conv string
	w.until("a DM with Alice", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": laptop.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	w.api("/api/dm/send", map[string]any{"conv": conv, "body": "dana before"})
	waitFor(t, "Alice has it", goHas(laptop, conv, "dana before", nil))
	if _, err := laptop.SendConv(ctx, conv, client.ConvOutgoing{Body: "alice before"}); err != nil {
		t.Fatal(err)
	}
	w.until("Alice's answer", func() bool { return dmMessage(w, conv, "alice before") != nil })

	// Eve (a raw device) knows Dana's person as it is before the link.
	evCode, _ := laptop.Invite(ctx, "eve", time.Hour, false)
	eve := newRawAgent(t, evCode, "pc")
	eveRoster := eve.roster("Eve")
	dPub, dRoster := eve.peer(phone)

	// The link, as the page shows it: this page's URL with the code in its fragment.
	l := w.api("/api/device/link", map[string]any{})
	url, _ := l["url"].(string)
	i := strings.Index(url, "#")
	if !strings.HasPrefix(url, base+"/#"+protocol.LinkPrefix) || i < 0 {
		t.Fatalf("link: %v", l)
	}
	tablet, err := client.JoinAndLink(ctx, filepath.Join(t.TempDir(), "tablet"), url[i+1:], "tablet")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tablet.Close() })
	runDaemon(t, tablet)
	var ask map[string]any
	w.until("the tablet's request", func() bool {
		ls, _ := w.api("/api/overview", nil)["links"].([]any)
		if len(ls) == 1 {
			ask = ls[0].(map[string]any)
		}
		return ask != nil
	})
	if ask["address"] != tablet.Address || ask["name"] != "tablet" || ask["state"] != "pending" || ask["fingerprint"] != tablet.Self().Fingerprint() {
		t.Fatalf("request: %v", ask)
	}
	if note := w.api("/api/device/decide", map[string]any{"id": ask["id"], "accept": true})["note"]; note != "tablet is now one of your devices." {
		t.Fatalf("note: %v", note)
	}
	ov := w.api("/api/overview", nil)
	if deviceCount(ov) != 2 || ov["links"] != nil {
		t.Fatalf("after approval: %v %v", ov["person"], ov["links"])
	}
	waitFor(t, "the tablet linked", func() bool { return tablet.LinkState().State == "linked" })
	w.until("history copied", func() bool {
		h, _ := w.api("/api/overview", nil)["history"].([]any)
		return len(h) == 1 && h[0].(map[string]any)["state"] == "done" && h[0].(map[string]any)["device"] == tablet.Address
	})
	history := func(m client.ConvMessage) bool { return m.History && m.SyncedFrom == phone }
	waitFor(t, "the tablet has Dana's message as hers", goHas(tablet, conv, "dana before", func(m client.ConvMessage) bool { return history(m) && m.Dir == "out" }))
	waitFor(t, "the tablet has Alice's message", goHas(tablet, conv, "alice before", func(m client.ConvMessage) bool { return history(m) && m.Dir == "in" }))

	// After: Alice's new message reaches the tablet; the tablet's reaches
	// Alice and the browser (as Dana's own, from the tablet).
	if _, err := laptop.SendConv(ctx, conv, client.ConvOutgoing{Body: "alice after"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the tablet has Alice's new one", goHas(tablet, conv, "alice after", nil))
	if _, err := tablet.SendConv(ctx, conv, client.ConvOutgoing{Body: "from dana's tablet"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "Alice has the tablet's", goHas(laptop, conv, "from dana's tablet", nil))
	w.until("the browser has the tablet's as Dana's", func() bool {
		m := dmMessage(w, conv, "from dana's tablet")
		return m != nil && m["dir"] == "out" && m["via"] == tablet.Address
	})

	// Eve sends to the roster before the link (her copies reach the phone
	// only): the phone forwards it to the tablet as history.
	root, rootJSON := eve.dmRoot(eveRoster, dPub, dRoster)
	fan := []envelope.Fan{{Person: eveRoster.Person, Roster: eveRoster.Hash()}, {Person: dRoster.Person, Roster: dRoster.Hash()}}
	eve.send(dPub, envelope.Inner{V: 2, Kind: "message", Body: "to the old devices", Conv: root.ID(), LID: protocol.NewID(), Root: rootJSON, Origin: "ui", Fan: fan})
	w.until("Eve's message", func() bool { return dmMessage(w, root.ID(), "to the old devices") != nil })
	waitFor(t, "the tablet has Eve's message, forwarded", goHas(tablet, root.ID(), "to the old devices", func(m client.ConvMessage) bool { return history(m) && m.Dir == "in" }))

	// Removed: one device again; the last one cannot be removed.
	if note := w.api("/api/device/remove", map[string]any{"address": tablet.Address})["note"]; note != tablet.Address+" is no longer one of your devices." {
		t.Fatalf("remove: %v", note)
	}
	if deviceCount(w.api("/api/overview", nil)) != 1 {
		t.Fatal("still two devices")
	}
	if v := w.call(map[string]any{"op": "api", "path": "/api/device/remove", "body": map[string]any{"address": phone}}); v["error"] == nil {
		t.Fatal("the last device was removed")
	}
	if v := w.call(map[string]any{"op": "api", "path": "/api/device/service", "body": map[string]any{}}); v["error"] == nil {
		t.Fatal("a browser became a service")
	}
}
