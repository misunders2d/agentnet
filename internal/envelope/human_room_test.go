package envelope

import (
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// roomFixture signs (with a zero-seed key: shape only, as the envelope
// checks) a DM root, a group root and participations captured in a
// HumanTurn: a person guest and agent room participants.
type roomFixture struct {
	key         ed25519.PrivateKey
	dm, group   protocol.ConvRoot
	dmRaw, gRaw json.RawMessage
}

func newRoomFixture(t *testing.T) roomFixture {
	t.Helper()
	f := roomFixture{key: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	fp := "01234567-89abcdef-01234567-89abcdef"
	members := []protocol.ConvMember{{Person: strings.Repeat("1", 32), Roster: strings.Repeat("a", 64)}, {Person: strings.Repeat("2", 32), Roster: strings.Repeat("b", 64)}}
	creator := protocol.ConvCreator{Person: members[0].Person, Roster: members[0].Roster, Address: "fixture/alice", Fingerprint: fp}
	f.dm = protocol.ConvRoot{V: protocol.ConvRootVersion, Kind: protocol.ConvKindDM, Creator: creator, Members: members, Nonce: strings.Repeat("3", 32), Created: 1790000000}
	f.dm.Sign(f.key)
	f.group = protocol.ConvRoot{V: protocol.GroupRootVersion, Kind: protocol.ConvKindGroup, Creator: creator, Members: members, Nonce: strings.Repeat("4", 32), Created: 1790000000,
		Realm: strings.Repeat("5", 32), Title: "Room", Admins: []string{members[0].Person}}
	f.group.Sign(f.key)
	f.dmRaw, _ = json.Marshal(f.dm)
	f.gRaw, _ = json.Marshal(f.group)
	for _, r := range []protocol.ConvRoot{f.dm, f.group} {
		raw, _ := json.Marshal(r)
		if _, err := protocol.ParseConvRoot(raw); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// scope is one captured participation of conv: role "" or human, audience
// conversation or room; it returns its audience entry and proof.
func (f roomFixture) scope(conv, role, audience string) (HumanScope, []protocol.ParticipationEvent) {
	fp := "01234567-89abcdef-01234567-89abcdef"
	inv := protocol.ParticipationEvent{V: 1, Conv: conv, PID: protocol.NewID(), Type: protocol.EventInvite, TS: 1790000000,
		Author: protocol.EventAuthor{Person: strings.Repeat("1", 32), Roster: strings.Repeat("a", 64), Address: "fixture/alice", Fingerprint: fp},
		Host:   &protocol.ParticipationHost{Person: strings.Repeat("6", 32), Address: "fixture/carol", Fingerprint: fp}, Audience: audience, Role: role}
	inv.Sign(f.key)
	s := protocol.ScopeOf(inv, 1790000050)
	s.Sign(f.key)
	acc := protocol.ParticipationEvent{V: 1, Conv: conv, PID: inv.PID, Type: protocol.EventAccept, Prev: inv.Hash(), TS: 1790000100,
		Author: protocol.EventAuthor{Person: inv.Host.Person, Roster: strings.Repeat("c", 64), Address: inv.Host.Address, Fingerprint: fp}}
	acc.Sign(f.key)
	return HumanScope{PID: inv.PID, Invite: inv.Hash(), Decision: acc.Hash()}, []protocol.ParticipationEvent{s, acc}
}

func (f roomFixture) turn(conv, author string, parts ...[]any) *HumanTurn {
	h := &HumanTurn{AuthorPID: author}
	for _, p := range parts {
		h.Audience = append(h.Audience, p[0].(HumanScope))
		h.Proof = append(h.Proof, p[1].([]protocol.ParticipationEvent)...)
	}
	return h
}

func pair(s HumanScope, proof []protocol.ParticipationEvent) []any { return []any{s, proof} }

// ROOM_V1 §2.3: a captured audience rides on group turns too; an agent
// room participant authors only requests, labelled as an agent, and a
// person never carries an agent origin; an agent's proof scope must be a
// room's; an edit carries its turn's captured audience.
func TestHumanTurnRoomShapes(t *testing.T) {
	f := newRoomFixture(t)
	for name, root := range map[string]json.RawMessage{"dm": f.dmRaw, "group": f.gRaw} {
		var r protocol.ConvRoot
		json.Unmarshal(root, &r)
		conv := r.ID()
		guestAudience := protocol.AudienceConversation
		if r.Kind == protocol.ConvKindGroup {
			guestAudience = protocol.AudienceRoom
		}
		guest := pair(f.scope(conv, protocol.RoleHuman, guestAudience))
		agent := pair(f.scope(conv, "", protocol.AudienceRoom))
		assistant := protocol.NewID()
		base := Inner{V: Version2, ID: protocol.NewID(), From: "fixture/alice", To: "fixture/bob", TS: 1790000200, Kind: KindMessage, Body: "hi",
			Conv: conv, LID: protocol.NewID(), Root: root, Origin: OriginUI}
		target := &Target{Address: "fixture/dave", Fingerprint: "01234567-89abcdef-01234567-89abcdef"}
		ok := map[string]Inner{}
		member := base
		member.Human = f.turn(conv, "", guest, agent)
		ok["a member's turn"] = member
		g := base
		g.PID, g.Human = guest[0].(HumanScope).PID, f.turn(conv, guest[0].(HumanScope).PID, guest, agent)
		ok["a guest's turn"] = g
		ask := base
		ask.Kind, ask.Target, ask.PID, ask.Origin, ask.Emotion = KindQuestion, target, assistant, "agent:claude", "curious"
		ask.Human = f.turn(conv, agent[0].(HumanScope).PID, guest, agent)
		ok["an agent participant's request"] = ask
		task := ask
		task.Kind, task.Origin, task.Emotion = KindTask, OriginUI, ""
		task.Human = f.turn(conv, guest[0].(HumanScope).PID, guest, agent)
		ok["a guest's request"] = task
		out := base
		out.Kind, out.PID, out.ReplyTo, out.Origin, out.Emotion = KindAnswer, agent[0].(HumanScope).PID, protocol.NewID(), "agent:claude", "calm"
		out.Human = f.turn(conv, "", guest, agent)
		ok["an agent participant's output"] = out
		for what, in := range ok {
			if err := checkVersion2(in); err != nil {
				t.Errorf("%s: %s refused: %v", name, what, err)
			}
		}
		bad := map[string]Inner{}
		x := ask
		x.Human = f.turn(conv, guest[0].(HumanScope).PID, guest, agent)
		bad["an agent origin for a person"] = x
		x = ask
		x.Origin, x.Emotion = OriginUI, ""
		bad["an agent's request without its agent origin"] = x
		x = base
		x.PID, x.Human = agent[0].(HumanScope).PID, f.turn(conv, agent[0].(HumanScope).PID, guest, agent)
		bad["an agent's ordinary turn"] = x
		x = member
		x.Human = f.turn(conv, "", guest, pair(f.scope(conv, "", protocol.AudienceConversation)))
		bad["an assistant (conversation scope) as audience"] = x
		x = member
		x.Root = f.dmRaw
		if name == "dm" {
			x.Root = f.gRaw
		}
		bad["another conversation's root"] = x
		for what, in := range bad {
			if err := checkVersion2(in); err == nil {
				t.Errorf("%s: %s accepted", name, what)
			}
		}
		// An edit (revision or retraction) carrying its turn's audience.
		edit := Inner{V: Version3, ID: protocol.NewID(), From: "fixture/alice", To: "fixture/bob", TS: 1790000300, Kind: KindMessage, Sub: SubRevision,
			Body: `{"rev":1,"text":"fixed"}`, Conv: conv, LID: protocol.NewID(), Ref: &Ref{ID: g.LID, Fingerprint: "01234567-89abcdef-01234567-89abcdef"},
			Human: f.turn(conv, guest[0].(HumanScope).PID, guest, agent)}
		if err := checkVersion2(edit); err != nil {
			t.Errorf("%s: a guest's edit refused: %v", name, err)
		}
		retract := edit
		retract.Sub, retract.Body, retract.Human = SubRetraction, `{}`, f.turn(conv, "", guest)
		if err := checkVersion2(retract); err != nil {
			t.Errorf("%s: a member's retraction refused: %v", name, err)
		}
		for what, change := range map[string]func(*Inner){
			"with a participation":   func(in *Inner) { in.PID = guest[0].(HumanScope).PID },
			"with a root":            func(in *Inner) { in.Root = root },
			"a reaction":             func(in *Inner) { in.Sub, in.Body = SubReaction, `{"emoji":"👍","op":"add","n":1}` },
			"outside its audience":   func(in *Inner) { in.Human = f.turn(conv, protocol.NewID(), guest) },
			"without a conversation": func(in *Inner) { in.Conv, in.LID = "", "" },
		} {
			x := edit
			change(&x)
			if err := checkVersion2(x); err == nil {
				t.Errorf("%s: an edit %s accepted", name, what)
			}
		}
	}
}
