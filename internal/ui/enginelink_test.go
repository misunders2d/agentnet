package ui

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
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

// Two ordinary people use the same signed transport and existing history,
// controls, read and reminder APIs on native and browser devices. The bootstrap
// administrator only issues their non-admin invitations. Node keeps the browser
// store in memory; the companion real-IndexedDB tests cover durable reopening.
func TestTwoOrdinaryPeopleLinkedStateJourney(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + h.Addr
	admin, err := client.Join(ctx, filepath.Join(t.TempDir(), "admin"), testhub.BootstrapCode(t, dir), "issuer")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	type person struct {
		name, home string
		native     *client.Agent
		stop       func()
		browser    *engineNode
		later      *engineNode
		incoming   string
	}
	people := make([]*person, 0, 2)
	for _, name := range []string{"Alice", "Bob"} {
		code, err := admin.Invite(ctx, strings.ToLower(name), time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		p := &person{name: name, home: filepath.Join(t.TempDir(), name)}
		p.native, err = client.Join(ctx, p.home, code, "native")
		if err != nil {
			t.Fatal(err)
		}
		a := p.native
		t.Cleanup(func() { a.Close() })
		p.stop = runDaemon(t, a)
		if _, err = a.CreatePerson(ctx, name); err != nil {
			t.Fatal(err)
		}
		role, err := a.HubRole(ctx)
		if err != nil || role != protocol.RoleMember {
			t.Fatalf("%s must be an ordinary member: role=%s err=%v", name, role, err)
		}
		people = append(people, p)
	}
	alice, bob := people[0], people[1]
	var conv string
	waitFor(t, "ordinary members' DM", func() bool { conv, err = alice.native.CreateDM(ctx, bob.native.Address); return err == nil })
	send := func(p *person, body string) string {
		t.Helper()
		x, err := p.native.SendConv(ctx, conv, client.ConvOutgoing{Body: body})
		if err != nil {
			t.Fatal(err)
		}
		return x.LID
	}
	aLID := send(alice, "Alice's original")
	waitFor(t, "Bob's original history", goHas(bob.native, conv, "Alice's original", nil))
	bLID := send(bob, "Bob's original")
	waitFor(t, "Alice's original history", goHas(alice.native, conv, "Bob's original", nil))
	alice.incoming, bob.incoming = bLID, aLID
	goTurn := func(p *person, lid string) client.ConvMessage {
		ms, err := p.native.ConversationMessages(conv)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range ms {
			if m.LID == lid {
				return m
			}
		}
		return client.ConvMessage{}
	}
	browserTurn := func(w *engineNode, lid string) map[string]any {
		response := w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + conv})
		if response["error"] != nil {
			return nil // a newly linked device may still be receiving its root
		}
		v := response["v"].(map[string]any)
		for _, row := range v["messages"].([]any) {
			m := row.(map[string]any)
			if m["lid"] == lid {
				return m
			}
		}
		return nil
	}
	link := func(p *person, name string) *engineNode {
		t.Helper()
		offer, err := p.native.NewDeviceLink(ctx)
		if err != nil {
			t.Fatal(err)
		}
		o, err := protocol.DecodeLinkOffer(offer.Code)
		if err != nil {
			t.Fatal(err)
		}
		o.Invite = browserCode(t, o.Invite)
		w := startEngineNode(t, dir)
		w.ok(map[string]any{"op": "init", "base": base})
		address := w.ok(map[string]any{"op": "joinLink", "code": base + "/#" + o.Encode(), "name": name})["address"].(string)
		w.ok(map[string]any{"op": "start"})
		var request string
		waitFor(t, p.name+" linked-device request", func() bool {
			rs, _ := p.native.PendingLinks()
			for _, r := range rs {
				if r.State == "pending" && r.Address == address {
					request = r.ID
				}
			}
			return request != ""
		})
		if err := p.native.DecideLink(ctx, request, true); err != nil {
			t.Fatal(err)
		}
		w.until(p.name+" ordinary browser linked", func() bool {
			s := w.ok(map[string]any{"op": "status"})
			return s["connected"] == true && s["members"] == true && s["link"] == "linked"
		})
		profile := w.ok(map[string]any{"op": "streamState", "profile": true})["profile"].(map[string]any)
		if profile["self_role"] != protocol.RoleMember {
			t.Fatalf("%s browser gained administrator privilege: %v", p.name, profile["self_role"])
		}
		pub := w.devicePublic(t)
		w.until(p.name+" actual signed read/reminder capabilities", func() bool {
			snapshot := w.ok(map[string]any{"op": "streamState", "profile": true})
			raw, _ := json.Marshal(snapshot["profile"])
			var profile protocol.Profile
			if err := json.Unmarshal(raw, &profile); err != nil {
				t.Fatal(err)
			}
			return profile.Supports(address, pub.SignKey, protocol.CapReadSyncV2) && profile.Supports(address, pub.SignKey, protocol.CapReminderSync)
		})
		w.until(p.name+" granted history", func() bool { return browserTurn(w, aLID) != nil && browserTurn(w, bLID) != nil })
		return w
	}
	for _, p := range people {
		p.browser = link(p, "browser")
		if deviceCount(p.browser.api("/api/overview", nil)) != 2 {
			t.Fatal("initial ordinary person does not have exactly two linked devices")
		}
	}
	for i, p := range people {
		body := p.name + " sent from browser"
		p.browser.api("/api/dm/send", map[string]any{"conv": conv, "body": body})
		other := people[1-i]
		waitFor(t, p.name+" browser message reaches native and peer", func() bool {
			return goHas(p.native, conv, body, func(m client.ConvMessage) bool { return m.Dir == "out" && m.Via != "" })() && goHas(other.native, conv, body, func(m client.ConvMessage) bool { return m.Dir == "in" && m.Key != "" })() && dmMessage(other.browser, conv, body) != nil
		})
	}
	allControls := func(lid string, check func(client.Controls) bool, browserCheck func(map[string]any) bool) bool {
		for _, p := range people {
			if !check(goTurn(p, lid).Controls) || !browserCheck(browserTurn(p.browser, lid)) {
				return false
			}
			if p.later != nil && !browserCheck(browserTurn(p.later, lid)) {
				return false
			}
		}
		return true
	}
	for i, p := range people {
		m := browserTurn(p.browser, p.incoming)
		p.browser.api("/api/message/react", map[string]any{"conv": conv, "id": m["id"], "dir": "in", "emoji": "👍"})
		waitFor(t, p.name+" reaction on all four devices", func() bool {
			return allControls(p.incoming, func(c client.Controls) bool { return len(c.Reactions) == 1 }, func(m map[string]any) bool { r, _ := m["reactions"].([]any); return len(r) == 1 })
		})
		ref, err := p.native.RefOf(conv, goTurn(p, p.incoming).ID, "in")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = p.native.React(ctx, ref, "👍", true); err != nil {
			t.Fatal(err)
		}
		waitFor(t, p.name+" removed reaction on all devices", func() bool {
			return allControls(p.incoming, func(c client.Controls) bool { return len(c.Reactions) == 0 }, func(m map[string]any) bool { r, _ := m["reactions"].([]any); return len(r) == 0 })
		})
		ownLID := []string{aLID, bLID}[i]
		own := browserTurn(p.browser, ownLID)
		p.browser.api("/api/message/edit", map[string]any{"conv": conv, "id": own["id"], "dir": "out", "text": p.name + " edited from browser"})
		waitFor(t, p.name+" own-person edit on all devices", func() bool {
			return allControls(ownLID, func(c client.Controls) bool { return c.Edited && c.Text == p.name+" edited from browser" }, func(m map[string]any) bool { return m["edited"] == true && m["text"] == p.name+" edited from browser" })
		})
	}
	reminder := func(w *engineNode, lid string) map[string]any {
		for _, row := range w.ok(map[string]any{"op": "reminderState"})["rows"].([]any) {
			r := row.(map[string]any)
			fact := r["fact"].(map[string]any)
			if fact["lid"] == lid {
				return r
			}
		}
		return nil
	}
	markUnread := func(p *person, id string) {
		t.Helper()
		waitFor(t, p.name+" stable admitted attention snapshot", func() bool {
			err := p.native.MarkUnread(id, []string{id})
			if err != nil && !strings.Contains(err.Error(), "this chat changed; try again") {
				t.Fatal(err)
			}
			return err == nil
		})
	}
	for i, p := range people {
		m := goTurn(p, p.incoming)
		markUnread(p, m.ID)
		p.browser.until(p.name+" exact signed unread", func() bool { return browserTurn(p.browser, p.incoming)["unread"] == true })
		db, err := sql.Open("sqlite", "file:"+filepath.Join(p.home, "agent.db")+"?mode=ro")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		pub := p.browser.devicePublic(t)
		defer func() {
			if t.Failed() {
				t.Logf("%s reminder projection/facts: %v", p.name, p.browser.ok(map[string]any{"op": "reminderState"}))
				rows, err := db.Query(`SELECT sub,state,coalesce(error,'') FROM outbox WHERE recipient=? AND sub IN (?,?) ORDER BY rowid DESC LIMIT 6`, pub.Address, envelope.SubReadSync, envelope.SubReminderSync)
				if err == nil {
					defer rows.Close()
					for rows.Next() {
						var sub, state, reason string
						if rows.Scan(&sub, &state, &reason) == nil {
							t.Logf("%s own-state carrier: %s %s %s", p.name, sub, state, reason)
						}
					}
				}
			}
		}()
		var oldRead, oldReminder string
		capture := func(sub string, dst *string) {
			t.Helper()
			waitFor(t, p.name+" signed "+sub+" carrier", func() bool {
				var encoded string
				err := db.QueryRow(`SELECT body,envelope FROM outbox WHERE recipient=? AND sub=? AND body LIKE ? ORDER BY rowid DESC LIMIT 1`, pub.Address, sub, "%"+p.incoming+"%").Scan(dst, &encoded)
				if err != nil {
					return false
				}
				var env envelope.Envelope
				if json.Unmarshal([]byte(encoded), &env) != nil || env.VerifySig(p.native.Self().SignKey) != nil {
					t.Fatal("native own-state carrier lacks its actual signature")
				}
				return true
			})
		}
		capture(envelope.SubReadSync, &oldRead)
		bm := browserTurn(p.browser, p.incoming)
		p.browser.api("/api/act", map[string]any{"do": "read", "ids": []any{bm["id"]}, "attention": map[string]any{bm["id"].(string): bm["attention"]}})
		waitFor(t, p.name+" browser read clears native attention", func() bool { return !goTurn(p, p.incoming).Unread })
		if _, err := p.native.SetReminder(m.ID, time.Now().Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		capture(envelope.SubReminderSync, &oldReminder)
		p.browser.until(p.name+" overdue reminder", func() bool { r := reminder(p.browser, p.incoming); return r != nil && r["overdue"] == true })
		p.browser.api("/api/remind", map[string]any{"id": bm["id"], "due": time.Now().Add(time.Hour).Unix()})
		waitFor(t, p.name+" snooze clears old native due attention", func() bool {
			r, ok, _ := p.native.Reminder(m.ID)
			return ok && !r.Overdue && r.Due > time.Now().Unix()+3000
		})
		p.browser.api("/api/remind/done", map[string]any{"id": bm["id"]})
		waitFor(t, p.name+" completion clears native reminder", func() bool { r, ok, _ := p.native.Reminder(m.ID); return ok && r.State == client.ReminderDone })
		// The other person's outbox copy is the same logical turn, but their
		// personal read and reminder registers remain independent.
		other := people[1-i]
		if r := reminder(other.browser, p.incoming); r != nil {
			t.Fatalf("%s's private reminder leaked to %s: %v", p.name, other.name, r)
		}
		p.browser.ok(map[string]any{"op": "offline", "on": true})
		markUnread(p, m.ID)
		marked := goTurn(p, p.incoming)
		if err := p.native.MarkReadWithAttention([]string{m.ID}, map[string]protocol.AttentionGeneration{m.ID: *marked.Attention}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.native.SetReminder(m.ID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := p.native.CancelReminder(m.ID); err != nil {
			t.Fatal(err)
		}
		ownLID := []string{aLID, bLID}[i]
		own := goTurn(p, ownLID)
		ref, err := p.native.RefOf(conv, own.ID, "out")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.native.Revise(ctx, ref, p.name+" edited while browser offline"); err != nil {
			t.Fatal(err)
		}
		newer := send(other, "newer unseen for "+p.name)
		waitFor(t, p.name+" newer native arrival", func() bool { return goTurn(p, newer).Unread })
		p.stop()
		if err := p.native.Close(); err != nil {
			t.Fatal(err)
		}
		p.native, err = client.Open(p.home)
		if err != nil {
			t.Fatal(err)
		}
		again := p.native
		t.Cleanup(func() { again.Close() })
		p.stop = runDaemon(t, again)
		p.browser.ok(map[string]any{"op": "offline", "on": false})
		p.browser.ok(map[string]any{"op": "reload", "base": base})
		p.browser.until(p.name+" offline/restart catch-up", func() bool {
			r := reminder(p.browser, p.incoming)
			n := browserTurn(p.browser, newer)
			return r != nil && r["state"] == client.ReminderCancelled && browserTurn(p.browser, p.incoming)["unread"] != true && n != nil && n["unread"] == true
		})
		waitFor(t, p.name+" offline native edit converges", func() bool {
			return allControls(ownLID, func(c client.Controls) bool { return c.Text == p.name+" edited while browser offline" }, func(m map[string]any) bool { return m["text"] == p.name+" edited while browser offline" })
		})
		// Re-sign the actual older payload under the same fixture key with a
		// fresh carrier ID: this exercises generation rejection, not just an
		// already-seen envelope shortcut. Repeat it to cover duplicate delivery.
		key, err := identity.Load(filepath.Join(p.home, "identity.json"))
		if err != nil {
			t.Fatal(err)
		}
		recipient, err := pub.Recipient()
		if err != nil {
			t.Fatal(err)
		}
		for sub, body := range map[string]string{envelope.SubReadSync: oldRead, envelope.SubReminderSync: oldReminder} {
			in := envelope.Inner{V: 2, ID: protocol.NewID(), From: p.native.Address, To: pub.Address, TS: time.Now().Unix(), Kind: envelope.KindMessage, Sub: sub, Replica: true, Body: body}
			env, err := envelope.Seal(in, key.Sign, recipient)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(env)
			for range 2 {
				result := p.browser.ok(map[string]any{"op": "replayOwnState", "envelope": string(raw)})
				if result["held"] != nil || result["inbox"] != nil {
					t.Fatalf("quiet own-state replay was quarantined or became a chat: %v", result)
				}
			}
		}
		if browserTurn(p.browser, p.incoming)["unread"] == true || reminder(p.browser, p.incoming)["state"] != client.ReminderCancelled || !goTurn(p, newer).Unread {
			t.Fatal("old or duplicate signed state resurrected cleared attention or consumed newer work")
		}
		fresh := link(p, "later-browser")
		fresh.until(p.name+" later link receives terminal shared state", func() bool {
			r := reminder(fresh, p.incoming)
			return r != nil && r["state"] == client.ReminderCancelled && browserTurn(fresh, p.incoming)["unread"] != true && browserTurn(fresh, ownLID)["text"] == p.name+" edited while browser offline"
		})
		p.later = fresh
		if !goTurn(p, newer).Unread || goTurn(p, p.incoming).Unread {
			t.Fatal("link history altered existing native attention")
		}
		for _, harness := range []string{"codex", "claude", "pi"} {
			at, err := p.native.Attention(client.HookEvent{Harness: harness, Session: "after-shared-state", Event: "SessionStart"})
			if err != nil || strings.Contains(at.Text, m.ID) || !strings.Contains(at.Text, goTurn(p, newer).ID) {
				t.Fatalf("%s %s hook resurrected read attention or lost newer arrival: %v %s", p.name, harness, err, at.Text)
			}
			if err := at.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		t.Logf("%s: ordinary native/browser read, snooze/done/cancel, offline/restart, fresh+duplicate stale signed carriers, and later-link state converged", p.name)
	}
	for i, p := range people {
		lid := []string{aLID, bLID}[i]
		if i == 0 {
			ref, err := p.native.RefOf(conv, goTurn(p, lid).ID, "out")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.native.Retract(ctx, ref, ""); err != nil {
				t.Fatal(err)
			}
		} else {
			m := browserTurn(p.browser, lid)
			p.browser.api("/api/message/delete", map[string]any{"conv": conv, "id": m["id"], "dir": "out"})
		}
		waitFor(t, p.name+" deletion on all six current devices", func() bool {
			return allControls(lid, func(c client.Controls) bool { return c.Deleted }, func(m map[string]any) bool { return m["deleted"] == true })
		})
	}
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
	laptopHome := filepath.Join(t.TempDir(), "laptop")
	laptop, err := client.Join(ctx, laptopHome, testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { laptop.Close() })
	runDaemon(t, laptop)
	if _, err = laptop.CreatePerson(ctx, "Alice"); err != nil {
		t.Fatal(err)
	}
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
	sentData, recvData := bytes.Repeat([]byte("laptop "), 400), bytes.Repeat([]byte("bob "), 500)
	if _, err := laptop.SendConv(ctx, conv, client.ConvOutgoing{Body: "laptop file", Files: []client.OutgoingFile{{Path: goFile(t, "l.txt", sentData)}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := bob.SendConv(ctx, conv, client.ConvOutgoing{Body: "bob file", Files: []client.OutgoingFile{{Path: goFile(t, "b.txt", recvData)}}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the laptop has Bob's file", func() bool { return len(goMessage(laptop, conv, "bob file").Attachments) == 1 })

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
	w.ok(map[string]any{"op": "holdFirstStream"})
	w.ok(map[string]any{"op": "start"})
	w.until("pending first stream barrier", func() bool {
		state := w.ok(map[string]any{"op": "streamState"})
		streams, _ := state["streams"].([]any)
		return len(streams) == 1 && streams[0].(map[string]any)["waitingLink"] == true && streams[0].(map[string]any)["membersHeader"] == nil
	})
	firstSession := w.ok(map[string]any{"op": "streamState"})["session"].(string)
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
	w.ok(map[string]any{"op": "releaseFirstStream"})
	w.until("linked as Alice on two devices", func() bool {
		o := w.api("/api/overview", nil)
		p, _ := o["person"].(map[string]any)
		return w.ok(map[string]any{"op": "status"})["link"] == "linked" && p != nil && p["label"] == "Alice" && deviceCount(o) == 2 && o["link"] == nil
	})

	// Approval ends the restricted stream; the normal member stream must
	// resume before it can supply the queued history and live messages.
	w.until("member stream after linked approval", func() bool {
		s := w.ok(map[string]any{"op": "status"})
		return s["connected"] == true && s["members"] == true
	})

	// The server has already approved before this first pending-local stream
	// opens. Both the member-header transition and transport reconnect must use
	// one logical browser run, with a valid signed rd2 record for every session.
	browserPublic := w.devicePublic(t)
	requireProfile := func() bool {
		snapshot := w.ok(map[string]any{"op": "streamState", "profile": true})
		raw, e := json.Marshal(snapshot["profile"])
		if e != nil {
			t.Fatal(e)
		}
		var profile protocol.Profile
		if e = json.Unmarshal(raw, &profile); e != nil {
			t.Fatal(e)
		}
		return snapshot["session"] == firstSession && len(profile.Sessions) == 1 && profile.Sessions[0] == firstSession && profile.Supports(tablet, browserPublic.SignKey, protocol.CapReadSyncV2)
	}
	w.until("one signed rd2 session after member transition", requireProfile)
	streamSnapshot := w.ok(map[string]any{"op": "streamState"})
	streams := streamSnapshot["streams"].([]any)
	if len(streams) < 2 || streams[0].(map[string]any)["membersHeader"] != "1" {
		t.Fatal("member-header race was not exercised", streamSnapshot)
	}
	beforeReconnect := w.ok(map[string]any{"op": "reconnectStream"})
	w.until("same session after transport reconnect", func() bool {
		state := w.ok(map[string]any{"op": "streamState"})
		runs, _ := state["streams"].([]any)
		return len(runs) > int(beforeReconnect["streams"].(float64)) && state["generation"].(float64) > beforeReconnect["generation"].(float64) && requireProfile()
	})
	t.Logf("browser logical session=%s member-header transition and reconnect retained one valid signed rd2 profile; typing generation advanced", firstSession)

	// The chats from before, as history from the laptop: the laptop's own
	// message is yours, Bob's is his.
	w.until("the history", func() bool {
		return dmMessage(w, conv, "before the tablet") != nil && dmMessage(w, conv, "bob before") != nil
	})
	mine, his := dmMessage(w, conv, "before the tablet"), dmMessage(w, conv, "bob before")
	if mine["dir"] != "out" || his["dir"] != "in" || mine["synced_from"] != laptop.Address || his["synced_from"] != laptop.Address || his["unread"] == true {
		t.Fatalf("history: %v / %v", mine, his)
	}

	// The files from before: asked for from the laptop, then opened here.
	for body, want := range map[string][]byte{"laptop file": sentData, "bob file": recvData} {
		w.until("the history of "+body, func() bool { m := dmMessage(w, conv, body); return m != nil && len(m["attachments"].([]any)) == 1 })
		m := dmMessage(w, conv, body)
		if a := m["attachments"].([]any)[0].(map[string]any); a["availability"] != "requestable" {
			t.Fatalf("%s: %v", body, a)
		}
		if v := w.call(map[string]any{"op": "openFile", "id": m["id"], "i": 0}); v["error"] == nil {
			t.Fatal("a history file opened before it was asked for")
		}
		w.api("/api/file/request", map[string]any{"id": m["id"], "index": 0})
		if a := dmMessage(w, conv, body)["attachments"].([]any)[0].(map[string]any); a["availability"] != "requested" {
			t.Fatalf("%s after asking: %v", body, a)
		}
		w.until("the laptop's answer for "+body, func() bool {
			a := dmMessage(w, conv, body)["attachments"].([]any)[0].(map[string]any)
			return a["availability"] == nil
		})
		if f := w.ok(map[string]any{"op": "openFile", "id": m["id"], "i": 0}); f["b64"] != base64.StdEncoding.EncodeToString(want) {
			t.Fatalf("%s: not the file", body)
		}
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

	// Cross-provider V2 journey. Only real APIs create marks; the read-only
	// connection observes the Go ledger and signed-carrier admission evidence.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(laptopHome, "agent.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := goMessage(laptop, conv, "bob after")
	if m.ID == "" || m.LID == "" || m.Key != bob.Self().Fingerprint() {
		t.Fatal("exact signed logical reference missing")
	}
	if err = laptop.MarkUnread(m.ID, []string{m.ID}); err != nil {
		t.Fatal(err)
	}
	marked := goMessage(laptop, conv, "bob after")
	if !marked.Unread || marked.Attention == nil || marked.Attention.Writer != laptop.Self().Fingerprint() {
		t.Fatal("Go unread generation missing")
	}
	generation := *marked.Attention
	w.until("signed Go V2 unread admitted by browser", func() bool {
		v := dmMessage(w, conv, "bob after")
		if v == nil {
			return false
		}
		g, ok := v["attention"].(map[string]any)
		return ok && v["unread"] == true && v["lid"] == m.LID && v["send_group_author"] == m.Key && g["rev"] == float64(generation.Rev) && g["writer"] == generation.Writer
	})
	browserM := dmMessage(w, conv, "bob after")
	browserID := browserM["id"].(string)
	w.api("/api/act", map[string]any{"do": "read", "ids": []string{browserID}})
	still := dmMessage(w, conv, "bob after")
	g, _ := still["attention"].(map[string]any)
	if still["unread"] != true || g["rev"] != float64(generation.Rev) || g["writer"] != generation.Writer {
		t.Fatal("tokenless browser read cleared deliberate unread")
	}
	browserFP := w.devicePublic(t).Fingerprint()
	w.api("/api/act", map[string]any{"do": "read", "ids": []string{browserID}, "attention": map[string]any{browserID: generation}})
	var revision int64
	var writer, state string
	waitFor(t, "browser V2 tombstone admitted by Go", func() bool {
		e := db.QueryRow(`SELECT state,revision,writer FROM attention_marks WHERE conv=? AND fingerprint=? AND lid=?`, conv, m.Key, m.LID).Scan(&state, &revision, &writer)
		return e == nil && state == "read" && revision == generation.Rev+1 && writer == browserFP
	})
	cleared := goMessage(laptop, conv, "bob after")
	if cleared.Unread || cleared.Attention != nil {
		t.Fatal("Go projection did not clear after exact browser generation")
	}
	// An admitted read-sync carrier is inert: its disposition is kept in
	// history_receipts, not as an inbox message. Match its actual signed
	// browser outbox ID to that disposition and the canonical register.
	carries := func(body string, want protocol.AttentionMark) bool {
		r, e := protocol.ParseReadSync([]byte(body))
		if e != nil || r.V != 2 {
			return false
		}
		for _, mark := range r.Marks {
			if mark == want {
				return true
			}
		}
		return false
	}
	findOutbound := func(want protocol.AttentionMark) string {
		t.Helper()
		rows, e := db.Query(`SELECT id,body FROM outbox WHERE sub=? AND recipient=? AND recipient_fp=? AND required_cap=?`, envelope.SubReadSync, tablet, browserFP, protocol.CapReadSyncV2)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		for rows.Next() {
			var id, body string
			if e = rows.Scan(&id, &body); e != nil {
				t.Fatal(e)
			}
			if carries(body, want) {
				return id
			}
		}
		if e = rows.Err(); e != nil {
			t.Fatal(e)
		}
		return ""
	}
	ref := protocol.ReadRef{Conv: conv, Fingerprint: m.Key, LID: m.LID}
	goMark := protocol.AttentionMark{ReadRef: ref, AttentionGeneration: generation, State: "unread"}
	browserMark := protocol.AttentionMark{ReadRef: ref, AttentionGeneration: protocol.AttentionGeneration{Rev: generation.Rev + 1, Writer: browserFP}, State: "read"}
	var outbound string
	var browserCarrier map[string]any
	waitFor(t, "both exact V2 outbox carriers", func() bool {
		outbound = findOutbound(goMark)
		browserCarrier, _ = w.ok(map[string]any{"op": "outbox", "readSync": map[string]any{"to": laptop.Address, "fingerprint": laptop.Self().Fingerprint(), "mark": browserMark}})["rec"].(map[string]any)
		return outbound != "" && browserCarrier != nil
	})
	var raw []byte
	if err = db.QueryRow(`SELECT envelope FROM outbox WHERE id=?`, outbound).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var signed envelope.Envelope
	if json.Unmarshal(raw, &signed) != nil || signed.ID != outbound || signed.From != laptop.Address || signed.To != tablet || signed.V != 2 || signed.Attn {
		t.Fatal("Go V2 encrypted carrier provenance missing")
	}
	if err = signed.VerifySig(laptop.Self().SignKey); err != nil {
		t.Fatal("Go outbound signature", err)
	}
	inbound, ok := browserCarrier["id"].(string)
	if !ok || !protocol.ValidID(inbound) {
		t.Fatal("browser carrier ID missing")
	}
	encoded, ok := browserCarrier["envelope"].(string)
	if !ok {
		t.Fatal("browser encrypted carrier missing")
	}
	var incoming envelope.Envelope
	if json.Unmarshal([]byte(encoded), &incoming) != nil || incoming.ID != inbound || incoming.From != tablet || incoming.To != laptop.Address || incoming.V != 2 || incoming.Attn {
		t.Fatal("browser encrypted carrier direction/ID missing")
	}
	laptopID, err := identity.Load(filepath.Join(laptopHome, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	inner, err := envelope.Open(incoming, laptopID, laptop.Address, w.devicePublic(t))
	if err != nil {
		t.Fatal("Go cannot verify/decrypt browser tombstone", err)
	}
	if inner.Kind != envelope.KindMessage || inner.Sub != envelope.SubReadSync || !inner.Replica || inner.Body != browserCarrier["body"] || !carries(inner.Body, browserMark) {
		t.Fatal("browser signed plaintext does not bind exact tombstone")
	}
	var acked int
	waitFor(t, "exact browser carrier admission receipt", func() bool {
		e := db.QueryRow(`SELECT acked FROM history_receipts WHERE id=?`, inbound).Scan(&acked)
		return e == nil && (acked == 0 || acked == 1)
	})
	var quarantined int
	if err = db.QueryRow(`SELECT count(*) FROM quarantine WHERE id=?`, inbound).Scan(&quarantined); err != nil || quarantined != 0 {
		t.Fatal("browser carrier quarantined", quarantined, err)
	}
	if err = db.QueryRow(`SELECT state,revision,writer FROM attention_marks WHERE conv=? AND fingerprint=? AND lid=?`, ref.Conv, ref.Fingerprint, ref.LID).Scan(&state, &revision, &writer); err != nil || state != "read" || revision != browserMark.Rev || writer != browserMark.Writer {
		t.Fatal("exact carrier lacks canonical tombstone", err)
	}
	t.Logf("linked V2 logical_ref=%s/%s/%s go_carrier=%s browser_carrier=%s unread_rev=%d go_writer=%s tombstone_rev=%d browser_writer=%s tokenless_preserved=true signatures_verified=true browser_decrypted_in_go=true receipt_admitted=true receipt_acked=%d quarantine=0", conv, m.Key, m.LID, outbound, inbound, generation.Rev, generation.Writer, revision, writer, acked)

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

// goMessage is the message of conv with body on a, or a zero one.
func goMessage(a *client.Agent, conv, body string) client.ConvMessage {
	ms, _ := a.ConversationMessages(conv)
	for _, m := range ms {
		if m.Body == body {
			return m
		}
	}
	return client.ConvMessage{}
}

// goFile writes data to a file of its own and returns its path.
func goFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// dropDelivered stops the Hub, removes the files of every delivered
// message (as its cleanup does once they are old enough) and starts it
// again at the same address.
func dropDelivered(t *testing.T, p *testhub.Proc) *testhub.Proc {
	t.Helper()
	p.Stop()
	m, err := hub.OpenMaintenance(p.Dir)
	if err != nil {
		t.Fatal(err)
	}
	gone, err := m.Cleanup(-time.Hour, time.Hour, time.Hour)
	m.Close()
	if err != nil || gone.Files == 0 {
		t.Fatalf("cleanup: %+v %v", gone, err)
	}
	return testhub.Start(t, p.Dir, p.Addr, "")
}

// The browser keeps what it needs to give a new device of its person the
// files of its chats: a copy of each file it sent, and the ciphertext of
// each it received. With the server's copies gone, a Go device linked
// afterwards asks it for both and gets the same bytes; opening a received
// file uses the kept copy too.
func TestBrowserEngineServesHistoryFiles(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.Start(t, dir, "127.0.0.1:0", "")
	base := "https://" + h.Addr
	laptop := personAgent(t, ctx, testhub.BootstrapCode(t, dir), "laptop", "Alice")
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	code, _ := laptop.Invite(ctx, "dana", time.Hour, false)
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "phone"})
	w.api("/api/person", map[string]any{"label": "Dana"})
	w.ok(map[string]any{"op": "start"})
	var conv string
	w.until("a DM with Alice", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": laptop.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	w.api("/api/dm/send", map[string]any{"conv": conv, "body": "hello"})
	waitFor(t, "Alice has the DM", goHas(laptop, conv, "hello", nil))
	received, sent := bytes.Repeat([]byte("alice's file "), 300), bytes.Repeat([]byte("dana's file "), 200)
	if _, err := laptop.SendConv(ctx, conv, client.ConvOutgoing{Body: "a file for you", Files: []client.OutgoingFile{{Path: goFile(t, "for-dana.txt", received)}}}); err != nil {
		t.Fatal(err)
	}
	w.until("Alice's file", func() bool { return dmMessage(w, conv, "a file for you") != nil })
	w.ok(map[string]any{"op": "sendFiles", "conv": conv, "body": "a file for alice", "files": []any{map[string]any{"name": "for-alice.txt", "b64": base64.StdEncoding.EncodeToString(sent)}}})
	waitFor(t, "Alice has Dana's file", func() bool { return len(goMessage(laptop, conv, "a file for alice").Attachments) == 1 })
	w.until("both kept here", func() bool { return w.ok(map[string]any{"op": "count", "store": "files"})["n"].(float64) == 2 })
	w.until("Dana's file delivered", func() bool { m := dmMessage(w, conv, "a file for alice"); return m != nil && m["state"] == "delivered" })

	h = dropDelivered(t, h)
	w.until("connected again", func() bool { return w.ok(map[string]any{"op": "status"})["connected"] == true })
	in := dmMessage(w, conv, "a file for you")
	if f := w.ok(map[string]any{"op": "openFile", "id": in["id"], "i": 0}); f["b64"] != base64.StdEncoding.EncodeToString(received) {
		t.Fatal("the kept received file does not open as it was")
	}

	l := w.api("/api/device/link", map[string]any{})
	url := l["url"].(string)
	tablet, err := client.JoinAndLink(ctx, filepath.Join(t.TempDir(), "tablet"), url[strings.Index(url, "#")+1:], "tablet")
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
	w.api("/api/device/decide", map[string]any{"id": ask["id"], "accept": true})
	requestable := func(body string) func() bool {
		return func() bool {
			m := goMessage(tablet, conv, body)
			return m.History && len(m.Attachments) == 1 && m.Attachments[0].Availability == "requestable"
		}
	}
	waitFor(t, "the received file in history", requestable("a file for you"))
	waitFor(t, "the sent file in history", requestable("a file for alice"))
	for body, want := range map[string][]byte{"a file for you": received, "a file for alice": sent} {
		m := goMessage(tablet, conv, body)
		if err := tablet.RequestFile(ctx, m.ID, 0); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the browser's answer for "+body, func() bool {
			a := goMessage(tablet, conv, body).Attachments
			return len(a) == 1 && a[0].Availability == ""
		})
		r, _, err := tablet.OpenAttachment(ctx, m.ID, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(r)
		r.Close()
		if !bytes.Equal(got, want) {
			t.Fatalf("%s: %d bytes, not the file", body, len(got))
		}
	}
}
