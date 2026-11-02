package ui

import (
	"encoding/json"
	"fmt"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
	"github.com/misunders2d/agentnet/internal/testhub"
	"path/filepath"
	"strings"
	"testing"
)

func TestMEL736OneBrowserParity(t *testing.T) {
	var records []string
	for i, id := range []string{"faeb3e71bb3aac661d198083239baa6f", "59c89e536113a047e24569888bcc106a", protocol.NewID()} {
		k, e := identity.Generate()
		if e != nil {
			t.Fatal(e)
		}
		r := protocol.PersonRoster{Person: id, Label: "Bohdan", Email: "bohdan@mellanni.com", Devices: []identity.Public{k.Public([]string{"bohdan/lost", "bohdan/new", "bohdan/third"}[i])}}
		r.Sign(k.Sign)
		raw, _ := json.Marshal(r)
		records = append(records, string(raw))
	}
	w := startEngineNode(t, t.TempDir())
	v := w.ok(map[string]any{"op": "mel736OneProbes", "rosters": records})
	if v["passed"] != true {
		t.Fatal(v)
	}
	t.Run("hub", testMEL736OneBrowserHub)
}

func testMEL736OneBrowserHub(t *testing.T) {
	issuer := testgoogle.New(t)
	relay := testhub.StartConfig(t, hub.Config{DataDir: filepath.Join(t.TempDir(), "hub"), GoogleWebClientID: testgoogle.ClientID, GoogleHTTP: issuer.Client}, "127.0.0.1:0")
	code := testhub.BootstrapCode(t, relay.Dir)
	inv, _ := protocol.DecodeInvite(code)
	w := startEngineNode(t, relay.Dir)
	w.ok(map[string]any{"op": "init", "base": inv.Hub})
	w.ok(map[string]any{"op": "join", "code": browserCode(t, code), "name": "browser"})
	w.ok(map[string]any{"op": "person", "label": "Colleague"})
	w.ok(map[string]any{"op": "start"})
	email := "bohdan@mellanni.com"
	w.api("/api/google/access", map[string]any{"email": email})
	join := func(name string) *client.Agent {
		t.Helper()
		home := t.TempDir()
		pub, e := client.GoogleDevice(home, name)
		if e != nil {
			t.Fatal(e)
		}
		a, e := client.JoinGoogle(t.Context(), home, client.GoogleOptions{Hub: inv.Hub, CertPEM: inv.CertPEM}, issuer.IDToken(t, pub, email, nil), name)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	old := join("lost")
	stopOld := runDaemon(t, old)
	before, _, _ := old.Person()
	var conv string
	w.until("browser DM capability", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": old.Address}})
		if v["error"] != nil {
			return false
		}
		conv = v["v"].(map[string]any)["id"].(string)
		return true
	})
	sent := w.api("/api/dm/send", map[string]any{"conv": conv, "body": "Before recovery from browser"})
	parent := sent["lid"].(string)
	w.until("old receives browser DM", func() bool {
		ms, _ := old.ConversationMessages(conv)
		for _, m := range ms {
			if m.Body == "Before recovery from browser" {
				return true
			}
		}
		return false
	})
	// The lost identity is the original administrator. Recovery must retain
	// ordinary messaging membership without transferring that signed role.
	created, e := old.CreateGroup(t.Context(), "Continuity group")
	if e != nil {
		t.Fatal(e)
	}
	group := created.State.Conv
	browser := w.api("/api/overview", nil)["person"].(map[string]any)["person"].(string)
	invitation, e := old.InviteGroup(t.Context(), group, browser, nil)
	if e != nil {
		t.Fatal(e)
	}
	w.until("browser group invitation", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/groups/invitations"})
		rows, _ := v["v"].([]any)
		for _, row := range rows {
			if row.(map[string]any)["id"] == invitation.ID {
				return true
			}
		}
		return false
	})
	w.api("/api/groups/decide", map[string]any{"id": invitation.ID, "accept": true})
	w.until("group membership published", func() bool {
		members, e := old.GroupMembers(group)
		return e == nil && len(members) == 2
	})
	w.until("browser group membership", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm?id=" + group})
		thread, _ := v["v"].(map[string]any)
		members, _ := thread["members"].([]any)
		return v["error"] == nil && len(members) == 2
	})
	stopOld()
	w.api("/api/google/access", map[string]any{"email": email, "remove": true})
	w.api("/api/google/access", map[string]any{"email": email})
	fresh := join("fresh")
	runDaemon(t, fresh)
	current, _, _ := fresh.Person()
	// Capability publication follows enrollment asynchronously. As in the
	// initial DM setup, wait for a usable current-person root before sending.
	w.until("recovered device conversation capabilities", func() bool {
		v := w.call(map[string]any{"op": "api", "path": "/api/dm/new", "body": map[string]any{"address": fresh.Address}})
		return v["error"] == nil
	})
	w.until("browser folds recovered person", func() bool {
		v := w.api("/api/overview", nil)
		oldShown, newShown := false, false
		for _, p := range v["people"].([]any) {
			person := p.(map[string]any)["person"]
			oldShown = oldShown || person == before.Person
			newShown = newShown || person == current.Person
		}
		return !oldShown && newShown
	})
	checkDM := func() bool {
		v := w.api("/api/dm?id="+conv, nil)
		count := 0
		for _, m := range v["messages"].([]any) {
			if strings.Contains(fmt.Sprint(m.(map[string]any)["event"]), "signed in again") {
				count++
			}
		}
		return count == 1
	}
	w.until("browser durable notice", checkDM)
	afterSent := w.api("/api/dm/send", map[string]any{"conv": conv, "body": "After recovery from browser", "reply_to": parent})
	var currentConv string
	w.until("new device receives browser reply", func() bool {
		cs, _ := fresh.Conversations()
		for _, c := range cs {
			ms, _ := fresh.ConversationMessages(c.ID)
			for _, m := range ms {
				if m.Body == "After recovery from browser" {
					currentConv = c.ID
					return m.ReplyTo == parent
				}
			}
		}
		return false
	})
	if currentConv == conv {
		t.Fatal("browser forged old root")
	}
	reply, e := fresh.SendConv(t.Context(), currentConv, client.ConvOutgoing{Body: "Recovered person answers browser", ReplyTo: afterSent["lid"].(string)})
	if e != nil {
		t.Fatal(e)
	}
	w.until("browser admits recovered DM author", func() bool {
		v := w.api("/api/dm?id="+conv, nil)
		for _, m := range v["messages"].([]any) {
			if m.(map[string]any)["lid"] == reply.LID {
				return true
			}
		}
		return false
	})
	w.api("/api/dm/send", map[string]any{"conv": group, "body": "Browser group continues"})
	w.until("new device receives browser group", func() bool {
		ms, _ := fresh.ConversationMessages(group)
		for _, m := range ms {
			if m.Body == "Browser group continues" {
				return true
			}
		}
		return false
	})
	groupReply, e := fresh.SendConv(t.Context(), group, client.ConvOutgoing{Body: "Recovered group author"})
	if e != nil {
		t.Fatal(e)
	}
	w.until("browser admits recovered group author", func() bool {
		v := w.api("/api/dm?id="+group, nil)
		for _, m := range v["messages"].([]any) {
			if m.(map[string]any)["lid"] == groupReply.LID {
				return true
			}
		}
		return false
	})
	view, e := NewLive(fresh).DM(group)
	if e != nil || view.Role != "member" || len(view.Members) != 2 {
		t.Fatal("Go rendered group projection", view.Role, len(view.Members), e)
	}
	for _, m := range view.Members {
		if m.Person == current.Person && m.Admin {
			t.Fatal("Go projected old administrator role onto recovered person")
		}
	}
	if _, e := fresh.RenameGroup(t.Context(), group, "Must not inherit admin"); e == nil || !strings.Contains(e.Error(), "administrator required") {
		t.Fatal("Go recovered administrator gate", e)
	}
	count := 0
	for _, m := range view.Messages {
		if strings.Contains(m.Event, "signed in again") {
			count++
		}
	}
	if count != 1 {
		t.Fatal("Go group recovery notice", count)
	}
	groupView := w.api("/api/dm?id="+group, nil)
	for _, m := range groupView["members"].([]any) {
		p := m.(map[string]any)
		if p["person"] == current.Person && p["admin"] == true {
			t.Fatal("browser projected old administrator role onto recovered person")
		}
	}
	count = 0
	for _, m := range groupView["messages"].([]any) {
		if strings.Contains(fmt.Sprint(m.(map[string]any)["event"]), "signed in again") {
			count++
		}
	}
	if count != 1 {
		t.Fatal("browser group recovery notice", count)
	}

	w.ok(map[string]any{"op": "reload", "base": inv.Hub})
	w.until("browser restart retains one notice", checkDM)
}
