package protocol

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Deterministic Go canonical/signature vectors for the additive group fields.
// The optional output path is private qualification evidence, never runtime.
func TestParticipationGroupVectors(t *testing.T) {
	member := vecInvite()
	member.Host.AgentID = vecSession
	member.Author.GroupAdmission = strings.Repeat("a", 64)
	member.Group = &ParticipationGroup{Seq: 7, Hash: strings.Repeat("b", 64), HostRole: "member", HostAdmission: strings.Repeat("c", 64), TaskAdmissions: []string{strings.Repeat("d", 64)}}
	member.Sign(vecKey())
	visitor := member
	visitor.Group = &ParticipationGroup{Seq: 7, Hash: member.Group.Hash, HostRole: "visitor", TaskAdmissions: member.Group.TaskAdmissions}
	visitor.Sign(vecKey())
	accept := ParticipationEvent{V: 1, Conv: visitor.Conv, PID: visitor.PID, Type: EventAccept, Prev: visitor.Hash(), Author: EventAuthor{Person: vecOther, Roster: vecRoster().Hash(), Address: "sergey/laptop", Fingerprint: vecFP}, TS: 1790000100}
	accept.Sign(vecKey())
	pub := vecKey().Public().(ed25519.PublicKey)
	type vector struct {
		Event     ParticipationEvent `json:"event"`
		Canonical string             `json:"canonical"`
		Hash      string             `json:"hash"`
		PublicKey []byte             `json:"public_key"`
	}
	var vectors []vector
	for _, event := range []ParticipationEvent{member, visitor, accept} {
		if err := event.Verify(pub); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(event)
		parsed, err := ParseParticipationEvent(raw)
		if err != nil || string(parsed.Canonical()) != string(event.Canonical()) {
			t.Fatalf("group canonical roundtrip: %v", err)
		}
		vectors = append(vectors, vector{event, string(event.Canonical()), event.Hash(), pub})
	}
	for name, mutate := range map[string]func(*ParticipationEvent){
		"missing inviter epoch": func(e *ParticipationEvent) { e.Author.GroupAdmission = "" },
		"missing host epoch":    func(e *ParticipationEvent) { e.Group.HostAdmission = "" },
		"visitor host epoch":    func(e *ParticipationEvent) { e.Group.HostRole = "visitor" },
		"inferred host role":    func(e *ParticipationEvent) { e.Group.HostRole = "" },
		"negative seq":          func(e *ParticipationEvent) { e.Group.Seq = -1 },
		"missing original hash": func(e *ParticipationEvent) { e.Group.Hash = "" },
		"task epoch count":      func(e *ParticipationEvent) { e.Group.TaskAdmissions = nil },
		"malformed task epoch":  func(e *ParticipationEvent) { e.Group.TaskAdmissions = []string{"wrong"} },
	} {
		changed := member
		scope := *member.Group
		changed.Group = &scope
		mutate(&changed)
		if changed.Validate() == nil || changed.Verify(pub) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	changed := accept
	changed.Group = visitor.Group
	if changed.Validate() == nil {
		t.Fatal("accept widened invite scope")
	}
	changed = member
	changed.Author.GroupAdmission = strings.Repeat("e", 64)
	if changed.Verify(pub) == nil {
		t.Fatal("changed inviter epoch retained signature")
	}
	if path := os.Getenv("AGENTNET_GROUP_PARTICIPATION_VECTORS"); path != "" {
		raw, err := json.MarshalIndent(vectors, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
}
