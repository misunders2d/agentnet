package protocol

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
)

// roomInvites are signed room invitations (ROOM_V1 §2.2): a DM follower
// agent, a group follower agent hosted by a member, and a group person
// guest (a room visitor), the last two with an end time.
func roomInvites() map[string]ParticipationEvent {
	dm := vecInvite()
	dm.Audience = AudienceRoom
	dm.Sign(vecKey())
	agent := vecInvite()
	agent.Audience, agent.Until = AudienceRoom, 1790003600
	agent.Author.GroupAdmission = strings.Repeat("a", 64)
	agent.Group = &ParticipationGroup{Seq: 7, Hash: strings.Repeat("b", 64), HostRole: "member", HostAdmission: strings.Repeat("c", 64), TaskAdmissions: []string{strings.Repeat("d", 64)}}
	agent.Sign(vecKey())
	guest := vecInvite()
	guest.Audience, guest.Until, guest.Role, guest.TaskKeys = AudienceRoom, 1790003600, RoleHuman, nil
	guest.Author.GroupAdmission = strings.Repeat("a", 64)
	guest.Group = &ParticipationGroup{Seq: 7, Hash: strings.Repeat("b", 64), HostRole: "visitor"}
	guest.Sign(vecKey())
	return map[string]ParticipationEvent{"dm follower": dm, "group follower": agent, "group guest": guest}
}

// Room is valid on invites and scopes; each scope is its invite's exact
// projection (audience, end time and group binding, never the grant, task
// keys or their admissions, or the note), and verifies.
func TestParticipationRoomEvents(t *testing.T) {
	pub := vecKey().Public().(ed25519.PublicKey)
	for name, inv := range roomInvites() {
		if err := inv.Verify(pub); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		s := ScopeOf(inv, 1790000050)
		s.Sign(vecKey())
		if err := s.Verify(pub); err != nil {
			t.Fatalf("%s scope: %v", name, err)
		}
		raw, _ := json.Marshal(s)
		if _, err := ParseParticipationEvent(raw); err != nil {
			t.Fatalf("%s scope: %v", name, err)
		}
		if !s.Projects(inv) || s.Audience != AudienceRoom || s.Until != inv.Until || (inv.Group == nil) != (s.Group == nil) {
			t.Fatalf("%s: scope %+v does not project %+v", name, s, inv)
		}
		if s.Group != nil && (s.Group.TaskAdmissions != nil || s.Group.Hash != inv.Group.Hash || s.Group.HostRole != inv.Group.HostRole) {
			t.Fatalf("%s: scope group %+v", name, s.Group)
		}
		for _, private := range []string{`"note"`, `"grant"`, `"task_keys"`, `"task_admissions"`} {
			if strings.Contains(string(raw), private) {
				t.Fatalf("%s: scope discloses %s: %s", name, private, raw)
			}
		}
		// A scope that disagrees with its invite on audience, group or end
		// time is not its projection.
		for what, change := range map[string]func(*ParticipationEvent){
			"audience": func(e *ParticipationEvent) {
				e.Audience, e.Until, e.Group, e.Author.GroupAdmission = AudienceConversation, 0, nil, ""
			},
			"until": func(e *ParticipationEvent) { e.Until++ },
			"until present or absent": func(e *ParticipationEvent) {
				if e.Until == 0 {
					e.Until = 1790003600
				} else {
					e.Until = 0
				}
			},
			"group": func(e *ParticipationEvent) {
				if e.Group == nil {
					e.Group, e.Author.GroupAdmission = &ParticipationGroup{Seq: 1, Hash: strings.Repeat("b", 64), HostRole: "visitor"}, strings.Repeat("a", 64)
					return
				}
				g := *e.Group
				g.Seq++
				e.Group = &g
			},
		} {
			bad := ScopeOf(inv, 1790000050)
			change(&bad)
			if bad.Validate() == nil && bad.Projects(inv) {
				t.Errorf("%s: a scope with another %s projects it", name, what)
			}
		}
	}
	// A conversation scope stays what it was; a group invitation to the
	// conversation audience still has no projection.
	legacy := ScopeOf(vecInvite(), 1790000050)
	if legacy.Audience != AudienceConversation || legacy.Until != 0 || legacy.Group != nil || !legacy.Projects(vecInvite()) {
		t.Fatalf("legacy scope %+v", legacy)
	}
	group := vecInvite()
	group.Author.GroupAdmission = strings.Repeat("a", 64)
	group.Group = &ParticipationGroup{Seq: 7, Hash: strings.Repeat("b", 64), HostRole: "visitor", TaskAdmissions: []string{strings.Repeat("d", 64)}}
	if s := ScopeOf(group, 1790000050); s.Validate() == nil || s.Projects(group) {
		t.Fatal("a group invitation to the conversation audience gained a projection")
	}
}

// The wire form of a room invitation and of a group person guest's parses
// (the same events as roomInvites, as JSON, without relying on new fields).
func TestParticipationRoomParsesFromJSON(t *testing.T) {
	for _, change := range []func(map[string]any){
		func(e map[string]any) { e["audience"] = "room" },
		func(e map[string]any) {
			e["audience"], e["role"] = "room", "human"
			delete(e, "task_keys")
			e["author"].(map[string]any)["group_admission"] = strings.Repeat("a", 64)
			e["group"] = map[string]any{"seq": 7, "hash": strings.Repeat("b", 64), "host_role": "visitor"}
		},
	} {
		var e map[string]any
		raw, _ := json.Marshal(vecInvite())
		json.Unmarshal(raw, &e)
		change(e)
		delete(e, "sig")
		raw, _ = json.Marshal(e)
		if _, err := ParseParticipationEvent(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
}

// A human guest in a group only as a room visitor; an end time only on a
// room invitation or its scope, never negative.
func TestParticipationRoomRefuses(t *testing.T) {
	invites := roomInvites()
	for name, c := range map[string]struct {
		base   string
		change func(*ParticipationEvent)
	}{
		"human group guest, conversation audience": {"group guest", func(e *ParticipationEvent) { e.Audience, e.Until = AudienceConversation, 0 }},
		"human group guest, member host role": {"group guest", func(e *ParticipationEvent) {
			e.Group.HostRole, e.Group.HostAdmission = "member", strings.Repeat("c", 64)
		}},
		"human group guest with task admissions": {"group guest", func(e *ParticipationEvent) {
			e.TaskKeys, e.Group.TaskAdmissions = []string{vecFP}, []string{strings.Repeat("d", 64)}
		}},
		"human group guest with an agent":       {"group guest", func(e *ParticipationEvent) { h := *e.Host; h.AgentID = vecSession; e.Host = &h }},
		"human group admission without a group": {"group guest", func(e *ParticipationEvent) { e.Group = nil }},
		"unknown audience":                      {"dm follower", func(e *ParticipationEvent) { e.Audience = "owner" }},
		"until on the conversation audience":    {"dm follower", func(e *ParticipationEvent) { e.Audience, e.Until = AudienceConversation, 1790003600 }},
		"negative until":                        {"group follower", func(e *ParticipationEvent) { e.Until = -1 }},
	} {
		e := invites[c.base]
		if e.Group != nil {
			g := *e.Group
			e.Group = &g
		}
		c.change(&e)
		if e.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for name, change := range map[string]func(*ParticipationEvent){
		"room scope with task admissions": func(e *ParticipationEvent) { e.Group.TaskAdmissions = []string{strings.Repeat("d", 64)} },
		"human room scope, member host": func(e *ParticipationEvent) {
			e.Role, e.Group.HostRole, e.Group.HostAdmission = RoleHuman, "member", strings.Repeat("c", 64)
			h := *e.Host
			h.AgentID = ""
			e.Host = &h
		},
		"conversation scope with a group": func(e *ParticipationEvent) { e.Audience, e.Until = AudienceConversation, 0 },
		"scope admission without a group": func(e *ParticipationEvent) { e.Group = nil },
		"scope until without a room": func(e *ParticipationEvent) {
			e.Audience, e.Group, e.Author.GroupAdmission = AudienceConversation, nil, ""
		},
	} {
		s := ScopeOf(invites["group follower"], 1790000050)
		change(&s)
		if s.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
	accept := ParticipationEvent{V: 1, Conv: vecInvite().Conv, PID: vecSession, Type: EventAccept, Prev: strings.Repeat("a", 64), TS: 1790000100,
		Author: EventAuthor{Person: vecOther, Roster: vecRoster().Hash(), Address: "sergey/laptop", Fingerprint: vecFP}, Until: 1790003600}
	if accept.Validate() == nil {
		t.Error("an accept with an end time accepted")
	}
}
