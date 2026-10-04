package ui

import (
	"crypto/ed25519"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// roomWire is a wire node with its device (dana) and a Go device (bob) that
// sign participation events (ROOM_V1 §2, §8).
type roomWire struct {
	w        *wireNode
	dana     identity.Public
	bobID    *identity.Identity
	bob      identity.Public
	conv     string
	author   protocol.EventAuthor
	danaHost protocol.ParticipationHost
	me       map[string]any
}

func startRoomWire(t *testing.T) roomWire {
	t.Helper()
	w := startWireNode(t)
	if m := w.ok(map[string]any{"op": "support"})["missing"].([]any); len(m) > 0 {
		t.Skipf("this node lacks %v", m)
	}
	r := roomWire{w: w, conv: strings.Repeat("c", 64)}
	setup := w.ok(map[string]any{"op": "setup", "address": "dana/phone"})
	if err := strictJSON([]byte(setup["public"].(string)), &r.dana); err != nil {
		t.Fatal(err)
	}
	r.bobID, _ = identity.Generate()
	r.bob = r.bobID.Public("bob/desk")
	bobPerson, danaPerson := protocol.NewID(), protocol.NewID()
	r.author = protocol.EventAuthor{Person: bobPerson, Roster: strings.Repeat("b", 64), Address: r.bob.Address, Fingerprint: r.bob.Fingerprint()}
	r.danaHost = protocol.ParticipationHost{Person: danaPerson, Address: r.dana.Address, Fingerprint: r.dana.Fingerprint()}
	r.me = map[string]any{"person": danaPerson, "roster": strings.Repeat("d", 64), "address": r.dana.Address, "fingerprint": r.dana.Fingerprint()}
	return r
}

// invites are Go-signed room invitations: a DM follower agent, a group
// follower agent hosted by a member, a group person guest; and a legacy
// conversation invite.
func (r roomWire) invites() map[string]protocol.ParticipationEvent {
	host := r.danaHost
	base := func() protocol.ParticipationEvent {
		h := host
		return protocol.ParticipationEvent{V: 1, Conv: r.conv, PID: protocol.NewID(), Type: protocol.EventInvite, Author: r.author, TS: time.Now().Unix(),
			Host: &h, Grant: []protocol.GrantRef{{LID: protocol.NewID(), Fingerprint: r.bob.Fingerprint()}}, Audience: protocol.AudienceRoom,
			TaskKeys: []string{r.bob.Fingerprint()}, Note: "help <&>"}
	}
	out := map[string]protocol.ParticipationEvent{}
	dm := base()
	out["dm follower"] = dm
	agent := base()
	agent.Until = time.Now().Unix() + 3600
	agent.Author.GroupAdmission = strings.Repeat("a", 64)
	agent.Group = &protocol.ParticipationGroup{Seq: 3, Hash: strings.Repeat("e", 64), HostRole: "member", HostAdmission: strings.Repeat("f", 64), TaskAdmissions: []string{strings.Repeat("9", 64)}}
	out["group follower"] = agent
	guest := base()
	guest.Role, guest.TaskKeys, guest.Until = protocol.RoleHuman, nil, time.Now().Unix()+60
	guest.Author.GroupAdmission = strings.Repeat("a", 64)
	guest.Group = &protocol.ParticipationGroup{Seq: 3, Hash: strings.Repeat("e", 64), HostRole: "visitor"}
	out["group guest"] = guest
	legacy := base()
	legacy.Audience = protocol.AudienceConversation
	out["legacy"] = legacy
	for k, e := range out {
		e.Sign(r.bobID.Sign)
		out[k] = e
	}
	return out
}

// ROOM_V1 §2.2, Go and the browser both ways: room invites and scopes
// (audience room, an end time, a group binding) parse with the same bytes
// and hash; each side's projection is the other's; what Go refuses the
// device refuses; and legacy events keep their bytes.
func TestBrowserRoomEventsMatchGo(t *testing.T) {
	r := startRoomWire(t)
	w := r.w
	both := func(what, raw string, goErr error) {
		t.Helper()
		if goErr == nil {
			t.Fatalf("%s: Go accepts it", what)
		}
		if v := w.call(map[string]any{"op": "parseEvent", "json": raw, "key": b64(r.bob.SignKey)}); v["error"] == nil {
			t.Errorf("%s: the device accepts what Go refuses (%v)", what, goErr)
		}
	}
	for name, inv := range r.invites() {
		scope := protocol.ScopeOf(inv, time.Now().Unix())
		scope.Sign(r.bobID.Sign)
		for _, e := range []protocol.ParticipationEvent{inv, scope} {
			if err := e.Verify(r.bob.SignKey); err != nil {
				t.Fatalf("%s %s: %v", name, e.Type, err)
			}
			got := w.ok(map[string]any{"op": "parseEvent", "json": marshal(t, e), "key": b64(r.bob.SignKey)})
			if got["hash"] != e.Hash() {
				t.Fatalf("%s %s: device hash %v, Go %s", name, e.Type, got["hash"], e.Hash())
			}
		}
		if got := w.ok(map[string]any{"op": "scopeOf", "invite": marshal(t, inv), "ts": scope.TS}); got["canonical"] != string(protocol.ScopeOf(inv, scope.TS).Canonical()) {
			t.Fatalf("%s: device projection\n%s\nGo\n%s", name, got["canonical"], protocol.ScopeOf(inv, scope.TS).Canonical())
		}
		if got := w.ok(map[string]any{"op": "projects", "scope": marshal(t, scope), "invite": marshal(t, inv)}); got["projects"] != true {
			t.Fatalf("%s: the device says Go's scope does not project its invite", name)
		}
		// A valid scope that disagrees on audience, end time or group binding.
		for what, change := range map[string]func(*protocol.ParticipationEvent){
			"until": func(e *protocol.ParticipationEvent) {
				if e.Audience == protocol.AudienceRoom {
					e.Until++
				} else {
					e.Audience, e.Until = protocol.AudienceRoom, 5
				}
			},
			"group seq": func(e *protocol.ParticipationEvent) {
				if e.Group == nil {
					e.Audience, e.Group, e.Author.GroupAdmission = protocol.AudienceRoom, &protocol.ParticipationGroup{Seq: 1, Hash: strings.Repeat("e", 64), HostRole: "visitor"}, strings.Repeat("a", 64)
					return
				}
				g := *e.Group
				g.Seq++
				e.Group = &g
			},
		} {
			bad := protocol.ScopeOf(inv, scope.TS)
			change(&bad)
			bad.Sign(r.bobID.Sign)
			if bad.Validate() != nil || bad.Projects(inv) {
				t.Fatalf("%s/%s: Go fixture %v", name, what, bad.Validate())
			}
			if got := w.ok(map[string]any{"op": "projects", "scope": marshal(t, bad), "invite": marshal(t, inv)}); got["projects"] != false {
				t.Errorf("%s: the device takes a scope with another %s as the invite's projection", name, what)
			}
		}
	}
	// The device's own room invitation and scope, in Go.
	for name, ev := range map[string]map[string]any{
		"dm follower": {"conv": r.conv, "pid": protocol.NewID(), "type": "invite", "author": r.me, "ts": time.Now().Unix(), "audience": "room", "until": time.Now().Unix() + 60,
			"host": map[string]any{"person": r.author.Person, "address": r.bob.Address, "fingerprint": r.bob.Fingerprint()}},
		"group guest": {"conv": r.conv, "pid": protocol.NewID(), "type": "invite", "author": withField(r.me, "group_admission", strings.Repeat("a", 64)), "ts": time.Now().Unix(), "audience": "room", "role": "human",
			"host": map[string]any{"person": r.author.Person, "address": r.bob.Address, "fingerprint": r.bob.Fingerprint()}, "group": map[string]any{"seq": 2, "hash": strings.Repeat("e", 64), "host_role": "visitor"}},
		"room scope": {"conv": r.conv, "pid": protocol.NewID(), "type": "scope", "prev": strings.Repeat("7", 64), "author": withField(r.me, "group_admission", strings.Repeat("a", 64)), "ts": time.Now().Unix(), "audience": "room", "until": 9,
			"host": map[string]any{"person": r.author.Person, "address": r.bob.Address, "fingerprint": r.bob.Fingerprint()}, "group": map[string]any{"seq": 2, "hash": strings.Repeat("e", 64), "host_role": "member", "host_admission": strings.Repeat("f", 64)}},
	} {
		v := w.ok(map[string]any{"op": "event", "event": ev})
		e, err := protocol.ParseParticipationEvent([]byte(v["json"].(string)))
		if err != nil || e.Verify(r.dana.SignKey) != nil || e.Hash() != v["hash"] || marshal(t, e) != v["json"] {
			t.Fatalf("%s: Go refuses or differs from the device's: %v\n%s\n%s", name, err, v["json"], marshal(t, e))
		}
	}
	// Refused alike.
	invites := r.invites()
	for what, c := range map[string]struct {
		base   string
		change func(*protocol.ParticipationEvent)
	}{
		"human group guest, conversation audience": {"group guest", func(e *protocol.ParticipationEvent) { e.Audience, e.Until = protocol.AudienceConversation, 0 }},
		"human group guest, member host role": {"group guest", func(e *protocol.ParticipationEvent) {
			e.Group = &protocol.ParticipationGroup{Seq: 3, Hash: strings.Repeat("e", 64), HostRole: "member", HostAdmission: strings.Repeat("f", 64)}
		}},
		"human group guest with task keys": {"group guest", func(e *protocol.ParticipationEvent) {
			e.TaskKeys = []string{r.bob.Fingerprint()}
			e.Group = &protocol.ParticipationGroup{Seq: 3, Hash: strings.Repeat("e", 64), HostRole: "visitor", TaskAdmissions: []string{strings.Repeat("9", 64)}}
		}},
		"human group admission, no group":    {"group guest", func(e *protocol.ParticipationEvent) { e.Group = nil }},
		"until on the conversation audience": {"legacy", func(e *protocol.ParticipationEvent) { e.Until = 60 }},
		"negative until":                     {"group follower", func(e *protocol.ParticipationEvent) { e.Until = -1 }},
		"until on an accept": {"legacy", func(e *protocol.ParticipationEvent) {
			*e = protocol.ParticipationEvent{V: 1, Conv: e.Conv, PID: e.PID, Type: protocol.EventAccept, Prev: strings.Repeat("7", 64), Author: e.Author, TS: e.TS, Audience: protocol.AudienceRoom, Until: 60}
		}},
		"room scope with task admissions": {"group follower", func(e *protocol.ParticipationEvent) {
			s := protocol.ScopeOf(*e, e.TS)
			s.Group.TaskAdmissions = []string{strings.Repeat("9", 64)}
			*e = s
		}},
		"conversation scope with a group": {"group follower", func(e *protocol.ParticipationEvent) {
			s := protocol.ScopeOf(*e, e.TS)
			s.Audience, s.Until = protocol.AudienceConversation, 0
			*e = s
		}},
		"human room scope, member host": {"group guest", func(e *protocol.ParticipationEvent) {
			s := protocol.ScopeOf(*e, e.TS)
			s.Group.HostRole, s.Group.HostAdmission = "member", strings.Repeat("f", 64)
			*e = s
		}},
	} {
		e := invites[c.base]
		c.change(&e)
		e.Sign(r.bobID.Sign)
		raw := marshal(t, e)
		_, goErr := protocol.ParseParticipationEvent([]byte(raw))
		both(what, raw, goErr)
	}
	// Legacy bytes do not change: the protocol's golden invite and accept
	// (TestParticipationVectors: the canonical bytes and hashes, key from an
	// all-zero seed) parse with those very hashes in Go and in the device.
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	golden := map[string]string{
		"e3664a7cce67553fd370fc8d5659d9a1102139dbf7e5ce5e1216e051173da383": `{"v":1,"conv":"2618a06e39982409d2effa23e5570480598e4727c1b6d81b36d43d420b96a3b9",` +
			`"pid":"00112233445566778899aabbccddeeff","type":"invite","prev":"","author":{"person":"0123456789abcdef0123456789abcdef",` +
			`"roster":"f78b94d0e4bf9f75df8a53076cb188f257acf1b660df845f0da670fdeef9f755","address":"vitalii/desk",` +
			`"fingerprint":"01234567-89abcdef-01234567-89abcdef"},"ts":1790000000,"host":{"person":"fedcba9876543210fedcba9876543210",` +
			`"address":"sergey/laptop","fingerprint":"01234567-89abcdef-01234567-89abcdef"},"grant":[{"lid":"11111111111111111111111111111111",` +
			`"fingerprint":"01234567-89abcdef-01234567-89abcdef"},{"lid":"22222222222222222222222222222222",` +
			`"fingerprint":"01234567-89abcdef-01234567-89abcdef"}],"audience":"conversation","task_keys":["01234567-89abcdef-01234567-89abcdef"],` +
			`"note":"check the deploy \u003c\u0026\u003e"}`,
		"fcc180b43f7bb73caa2dd4bd84f143e001622598dfbfeb2ade5507a41e5d6877": `{"v":1,"conv":"2618a06e39982409d2effa23e5570480598e4727c1b6d81b36d43d420b96a3b9",` +
			`"pid":"00112233445566778899aabbccddeeff","type":"accept","prev":"e3664a7cce67553fd370fc8d5659d9a1102139dbf7e5ce5e1216e051173da383",` +
			`"author":{"person":"fedcba9876543210fedcba9876543210","roster":"f78b94d0e4bf9f75df8a53076cb188f257acf1b660df845f0da670fdeef9f755",` +
			`"address":"sergey/laptop","fingerprint":"01234567-89abcdef-01234567-89abcdef"},"ts":1790000100}`,
	}
	for hash, canonical := range golden {
		var e protocol.ParticipationEvent
		if err := strictJSON([]byte(canonical), &e); err != nil {
			t.Fatal(err)
		}
		e.Sign(key)
		if e.Hash() != hash || string(e.Canonical()) != protocol.ParticipationDomain+canonical {
			t.Fatalf("Go's golden %s changed: %s", e.Type, e.Hash())
		}
		if got := w.ok(map[string]any{"op": "parseEvent", "json": marshal(t, e), "key": b64(key.Public().(ed25519.PublicKey))}); got["hash"] != hash {
			t.Fatalf("the device's golden %s hash %v", e.Type, got["hash"])
		}
	}
}

// ROOM_V1 §2.3, Go and the browser both ways: a captured audience on group
// turns, an agent room participant's labelled request, and edits carrying
// their turn's audience open on either side as the other sealed them; what
// Go refuses to seal (an agent origin for a person, an agent's unlabelled
// request or ordinary turn, an assistant's conversation scope as audience,
// an edit naming a participation) the device refuses too.
func TestBrowserRoomTurnsMatchGo(t *testing.T) {
	r := startRoomWire(t)
	w := r.w
	danaRecipient, _ := r.dana.Recipient()
	members := []protocol.ConvMember{{Person: r.author.Person, Roster: r.author.Roster}, {Person: r.danaHost.Person, Roster: strings.Repeat("d", 64)}}
	if members[0].Person > members[1].Person {
		members[0], members[1] = members[1], members[0]
	}
	creator := protocol.ConvCreator{Person: r.author.Person, Roster: r.author.Roster, Address: r.bob.Address, Fingerprint: r.bob.Fingerprint()}
	dm := protocol.ConvRoot{V: protocol.ConvRootVersion, Kind: protocol.ConvKindDM, Creator: creator, Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix()}
	dm.Sign(r.bobID.Sign)
	group := protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: creator, Members: members, Nonce: protocol.NewID(), Created: time.Now().Unix(),
		Realm: protocol.NewID(), Title: "Room <&>", Admins: []string{r.author.Person}}
	group.Sign(r.bobID.Sign)
	scope := func(conv, role, audience string) (envelope.HumanScope, []protocol.ParticipationEvent) {
		h := protocol.ParticipationHost{Person: protocol.NewID(), Address: "carol/desk", Fingerprint: r.bob.Fingerprint()}
		inv := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, Author: r.author, TS: time.Now().Unix(), Host: &h, Audience: audience, Role: role}
		inv.Sign(r.bobID.Sign)
		s := protocol.ScopeOf(inv, time.Now().Unix())
		s.Sign(r.bobID.Sign)
		acc := protocol.ParticipationEvent{V: 1, Conv: conv, PID: inv.PID, Type: protocol.EventAccept, Prev: inv.Hash(), TS: time.Now().Unix(),
			Author: protocol.EventAuthor{Person: h.Person, Roster: strings.Repeat("e", 64), Address: h.Address, Fingerprint: h.Fingerprint}}
		acc.Sign(r.bobID.Sign)
		return envelope.HumanScope{PID: inv.PID, Invite: inv.Hash(), Decision: acc.Hash()}, []protocol.ParticipationEvent{s, acc}
	}
	turn := func(author string, parts ...func() (envelope.HumanScope, []protocol.ParticipationEvent)) *envelope.HumanTurn {
		h := &envelope.HumanTurn{AuthorPID: author}
		for _, p := range parts {
			s, proof := p()
			h.Audience, h.Proof = append(h.Audience, s), append(h.Proof, proof...)
		}
		return h
	}
	// inner, as each side's seal takes it: the root as its JSON text.
	asMessage := func(in envelope.Inner) map[string]any {
		var m map[string]any
		strictJSON([]byte(marshal(t, in)), &m)
		if len(in.Root) != 0 {
			m["root"] = string(in.Root)
		}
		delete(m, "from")
		return m
	}
	for _, root := range []protocol.ConvRoot{dm, group} {
		conv, raw := root.ID(), json.RawMessage(marshal(t, root))
		guestAudience := protocol.AudienceConversation
		if root.Kind == protocol.ConvKindGroup {
			guestAudience = protocol.AudienceRoom
		}
		gs, gp := scope(conv, protocol.RoleHuman, guestAudience)
		as, ap := scope(conv, "", protocol.AudienceRoom)
		guest := func() (envelope.HumanScope, []protocol.ParticipationEvent) { return gs, gp }
		agent := func() (envelope.HumanScope, []protocol.ParticipationEvent) { return as, ap }
		assistant := func() (envelope.HumanScope, []protocol.ParticipationEvent) {
			return scope(conv, "", protocol.AudienceConversation)
		}
		base := envelope.Inner{V: envelope.Version2, TS: time.Now().Unix(), Kind: envelope.KindMessage, Body: "room <&>", Conv: conv, LID: protocol.NewID(), Root: raw, Origin: envelope.OriginUI}
		target := &envelope.Target{Address: "erin/desk", Fingerprint: r.bob.Fingerprint()}
		cases := map[string]struct {
			in envelope.Inner
			ok bool
		}{}
		add := func(what string, ok bool, change func(*envelope.Inner)) {
			in := base
			in.LID = protocol.NewID()
			change(&in)
			cases[root.Kind+": "+what] = struct {
				in envelope.Inner
				ok bool
			}{in, ok}
		}
		add("a member's turn", true, func(in *envelope.Inner) { in.Human = turn("", guest, agent) })
		add("a guest's turn", true, func(in *envelope.Inner) { in.PID, in.Human = gs.PID, turn(gs.PID, guest, agent) })
		add("an agent participant's request", true, func(in *envelope.Inner) {
			in.Kind, in.Target, in.PID, in.Origin, in.Emotion, in.Human = envelope.KindTask, target, protocol.NewID(), "agent:claude", "curious", turn(as.PID, guest, agent)
		})
		add("an agent origin for a person", false, func(in *envelope.Inner) {
			in.Kind, in.Target, in.PID, in.Origin, in.Emotion, in.Human = envelope.KindTask, target, protocol.NewID(), "agent:claude", "curious", turn(gs.PID, guest, agent)
		})
		add("an agent's unlabelled request", false, func(in *envelope.Inner) {
			in.Kind, in.Target, in.PID, in.Human = envelope.KindQuestion, target, protocol.NewID(), turn(as.PID, guest, agent)
		})
		add("an agent's ordinary turn", false, func(in *envelope.Inner) { in.PID, in.Human = as.PID, turn(as.PID, guest, agent) })
		add("an assistant's conversation scope as audience", false, func(in *envelope.Inner) { in.Human = turn("", guest, assistant) })
		add("a guest's edit", true, func(in *envelope.Inner) {
			in.V, in.Sub, in.Body, in.Root, in.Origin, in.Ref = envelope.Version3, envelope.SubRevision, `{"rev":1,"text":"fixed"}`, nil, "", &envelope.Ref{ID: protocol.NewID(), Fingerprint: r.bob.Fingerprint()}
			in.Human = turn(gs.PID, guest, agent)
		})
		add("a member's retraction", true, func(in *envelope.Inner) {
			in.V, in.Sub, in.Body, in.Root, in.Origin, in.Ref = envelope.Version3, envelope.SubRetraction, `{}`, nil, "", &envelope.Ref{ID: protocol.NewID(), Fingerprint: r.bob.Fingerprint()}
			in.Human = turn("", guest)
		})
		add("an edit naming a participation", false, func(in *envelope.Inner) {
			in.V, in.Sub, in.Body, in.Root, in.Origin, in.Ref = envelope.Version3, envelope.SubRevision, `{"rev":1,"text":"fixed"}`, nil, "", &envelope.Ref{ID: protocol.NewID(), Fingerprint: r.bob.Fingerprint()}
			in.PID, in.Human = gs.PID, turn(gs.PID, guest)
		})
		// As history items (the same shapes as a live turn, wire.parseHistory):
		// an agent's request and edits are kept; an agent origin for a person,
		// an agent's unlabelled request or ordinary turn are refused.
		for what, ok := range map[string]bool{"a guest's turn": true, "an agent participant's request": true, "a guest's edit": true, "a member's retraction": true,
			"an agent origin for a person": false, "an agent's unlabelled request": false, "an agent's ordinary turn": false} {
			in := cases[root.Kind+": "+what].in
			item := client.HistoryItem{V: 1, From: r.bob.Address, FromKey: r.bob.Fingerprint(), ID: protocol.NewID(), LID: in.LID, TS: in.TS, Kind: in.Kind, Body: in.Body,
				Sub: in.Sub, Origin: in.Origin, Emotion: in.Emotion, Target: in.Target, PID: in.PID, Ref: in.Ref, Human: in.Human, At: 1}
			v := w.call(map[string]any{"op": "history", "json": marshal(t, item)})
			if (v["error"] == nil) != ok || ok && v["json"] != marshal(t, item) {
				t.Errorf("%s: %s as history: %v, want kept %v", root.Kind, what, v, ok)
			}
		}
		for what, c := range cases {
			// Go to the device.
			in := c.in
			in.ID, in.From, in.To = protocol.NewID(), r.bob.Address, r.dana.Address
			env, goErr := envelope.Seal(in, r.bobID.Sign, danaRecipient)
			if (goErr == nil) != c.ok {
				t.Fatalf("%s: Go seal error %v, want ok %v", what, goErr, c.ok)
			}
			if goErr == nil {
				if v := w.call(map[string]any{"op": "open", "envelope": marshal(t, env), "from": publicJSON(t, r.bob)}); v["error"] != nil {
					t.Errorf("%s: the device cannot open Go's: %v", what, v["error"])
				}
			}
			// The device to Go.
			in.ID, in.From, in.To = protocol.NewID(), r.dana.Address, r.bob.Address
			v := w.call(map[string]any{"op": "seal", "to": publicJSON(t, r.bob), "message": asMessage(in)})
			if (v["error"] == nil) != c.ok {
				t.Errorf("%s: the device's seal error %v, want ok %v", what, v["error"], c.ok)
				continue
			}
			if c.ok {
				var env envelope.Envelope
				if err := strictJSON([]byte(v["envelope"].(string)), &env); err != nil {
					t.Fatal(err)
				}
				if _, err := envelope.Open(env, r.bobID, r.bob.Address, r.dana); err != nil {
					t.Errorf("%s: Go cannot open the device's: %v", what, err)
				}
			}
		}
	}
}

// The browser engine resolves room participations as the core does
// (testdata/room_engine_check.mjs; client TestRoomEvents*).
func TestBrowserRoomEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/room_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PASS room engine") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

// The browser engine reads room turns as the core does
// (testdata/room_receiver_engine_check.mjs; client TestRoom*InADM,
// TestRoomGroupTurns).
func TestBrowserRoomReceiverEngine(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	out, err := exec.Command(node, "testdata/room_receiver_engine_check.mjs").CombinedOutput()
	if err != nil || !strings.Contains(string(out), "PASS room receiver engine") {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
}

func withField(m map[string]any, k string, v any) map[string]any {
	out := map[string]any{}
	for x, y := range m {
		out[x] = y
	}
	out[k] = v
	return out
}
