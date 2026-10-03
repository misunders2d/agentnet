package client

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"strings"
	"testing"
)

func humanVector() (protocol.ParticipationEvent, protocol.ParticipationEvent, *envelope.HumanTurn) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	fp := "01234567-89abcdef-01234567-89abcdef"
	invite := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("a", 64), PID: strings.Repeat("1", 32), Type: protocol.EventInvite, TS: 1790000000, Author: protocol.EventAuthor{Person: strings.Repeat("2", 32), Roster: strings.Repeat("b", 64), Address: "fixture/alice", Fingerprint: fp}, Host: &protocol.ParticipationHost{Person: strings.Repeat("3", 32), Address: "fixture/carol", Fingerprint: fp}, Audience: protocol.AudienceConversation, Grant: []protocol.GrantRef{{LID: strings.Repeat("4", 32), Fingerprint: fp}}, Note: "human help <&>", Role: protocol.RoleHuman}
	invite.Sign(key)
	scope := protocol.ScopeOf(invite, 1790000050)
	scope.Sign(key)
	accept := protocol.ParticipationEvent{V: 1, Conv: invite.Conv, PID: invite.PID, Type: protocol.EventAccept, Prev: invite.Hash(), TS: 1790000100, Author: protocol.EventAuthor{Person: invite.Host.Person, Roster: strings.Repeat("c", 64), Address: invite.Host.Address, Fingerprint: fp}}
	accept.Sign(key)
	human := &envelope.HumanTurn{AuthorPID: invite.PID, Audience: []envelope.HumanScope{{PID: invite.PID, Invite: invite.Hash(), Decision: accept.Hash()}}, Proof: []protocol.ParticipationEvent{scope, accept}}
	return invite, accept, human
}
func TestHumanWireVectorsAndBounds(t *testing.T) {
	invite, accept, human := humanVector()
	scope := human.Proof[0]
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	pub := key.Public().(ed25519.PublicKey)
	for _, e := range []protocol.ParticipationEvent{invite, scope, accept} {
		if err := e.Verify(pub); err != nil {
			t.Fatal(err)
		}
	}
	if err := human.Validate(invite.Conv); err != nil {
		t.Fatal(err)
	}
	// The public projection carries none of the invitation's private parts,
	// and a proof carrying the invitation itself is refused.
	raw, _ := json.Marshal(scope)
	for _, private := range []string{invite.Note, invite.Grant[0].LID, `"note"`, `"grant"`, `"task_keys"`} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("scope discloses %q: %s", private, raw)
		}
	}
	if !scope.Projects(invite) {
		t.Fatal("scope does not project its invite")
	}
	leaky := *human
	leaky.Proof = []protocol.ParticipationEvent{invite, accept}
	if leaky.Validate(invite.Conv) == nil {
		t.Fatal("private invitation accepted as human proof")
	}
	for _, change := range []func(*protocol.ParticipationEvent){func(e *protocol.ParticipationEvent) { e.Note = invite.Note }, func(e *protocol.ParticipationEvent) { e.Grant = invite.Grant }, func(e *protocol.ParticipationEvent) { e.TaskKeys = []string{e.Author.Fingerprint} }} {
		bad := scope
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("scope with private invitation content accepted")
		}
	}
	ordinary := envelope.Inner{Conv: invite.Conv, LID: strings.Repeat("5", 32), Kind: envelope.KindMessage, Body: "ordinary guest-period turn", Origin: envelope.OriginUI, PID: invite.PID, Human: human}
	copy := ordinary
	copy.ID = "different-copy"
	copy.To = "fixture/bob"
	if contentHash(copy) != contentHash(ordinary) {
		t.Fatal("physical recipient changed logical identity")
	}
	wrong := *human
	wrong.Audience = append(append([]envelope.HumanScope{}, human.Audience...), human.Audience[0])
	if wrong.Validate(invite.Conv) == nil {
		t.Fatal("duplicate scope accepted")
	}
	wrong = *human
	wrong.Proof = human.Proof[:1]
	if wrong.Validate(invite.Conv) == nil {
		t.Fatal("missing acceptance proof accepted")
	}
	for _, change := range []func(*protocol.ParticipationEvent){func(e *protocol.ParticipationEvent) { e.TaskKeys = []string{e.Author.Fingerprint} }, func(e *protocol.ParticipationEvent) { h := *e.Host; h.AgentID = strings.Repeat("6", 32); e.Host = &h }, func(e *protocol.ParticipationEvent) { e.Role = "member" }} {
		bad := invite
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("human execution/member authority accepted")
		}
	}
	data, _ := json.Marshal(map[string]any{"public_key": hex.EncodeToString(pub), "invite": invite, "scope": scope, "accept": accept, "invite_canonical": string(invite.Canonical()), "scope_canonical": string(scope.Canonical()), "accept_canonical": string(accept.Canonical()), "invite_hash": invite.Hash(), "scope_hash": scope.Hash(), "accept_hash": accept.Hash(), "human": human, "human_json": humanJSON(human), "logical": ordinary, "content_hash": contentHash(ordinary)})
	t.Log("HUMAN_VECTOR_JSON " + string(data))
}
