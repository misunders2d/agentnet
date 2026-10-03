package ui

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// People are invited, join, leave or are removed; a guest never sees a lone
// shared invitation, each signed event is one row, and an original's onward
// copy of another person's event adds none. Agent wording is unchanged.
func TestHumanTimelineWordingAndInertProof(t *testing.T) {
	person := func(c string) string { return strings.Repeat(c, 32) }
	alice := PersonView{Person: person("a"), Label: "Alice", Address: "alice/laptop"}
	bob := PersonView{Person: person("b"), Label: "Bob", Address: "bob/laptop"}
	dana := PersonView{Person: person("d"), Label: "Dana", Address: "dana/desk"}
	pid := strings.Repeat("2", 32)
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	const fp = "01234567-89abcdef-01234567-89abcdef"
	carol := client.ParticipationInfo{PID: pid, Role: protocol.RoleHuman, State: client.PartActive, Decision: strings.Repeat("9", 64),
		Host: client.PersonInfo{Person: person("c"), Label: "Carol", Address: "carol/phone"}}
	ev := func(typ string, author PersonView) protocol.ParticipationEvent {
		e := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("1", 64), PID: pid, Type: typ, Prev: strings.Repeat("3", 64), TS: 1790000000,
			Author: protocol.EventAuthor{Person: author.Person, Roster: strings.Repeat("5", 64), Address: author.Address, Fingerprint: fp}}
		if typ == protocol.EventInvite {
			e.Prev, e.Audience = "", protocol.AudienceConversation
			e.Role, e.Host = protocol.RoleHuman, &protocol.ParticipationHost{Person: carol.Host.Person, Address: carol.Host.Address, Fingerprint: fp}
		}
		e.Sign(key)
		return e
	}
	carolPV := PersonView{Person: carol.Host.Person, Address: carol.Host.Address}
	member := dmPeople{me: alice, peer: bob, role: "member", humans: map[string]client.ParticipationInfo{pid: carol}}
	for e, want := range map[*protocol.ParticipationEvent]string{
		ptr(ev(protocol.EventInvite, bob)):      "Bob invited Carol into this DM.",
		ptr(ev(protocol.EventAccept, carolPV)):  "Carol joined this DM.",
		ptr(ev(protocol.EventDismiss, carolPV)): "Carol left this DM.",
		ptr(ev(protocol.EventDismiss, alice)):   "You removed Carol from this DM.",
	} {
		if got := eventText(marshal(t, *e), member); got != want {
			t.Fatalf("member: %q, want %q", got, want)
		}
	}
	self := carol
	self.HostHere = true
	guestSelf := dmPeople{me: carolPV, peer: alice, role: "visitor", humans: map[string]client.ParticipationInfo{pid: self}}
	for e, want := range map[*protocol.ParticipationEvent]string{
		ptr(ev(protocol.EventInvite, bob)):     "Bob invited you into this DM.",
		ptr(ev(protocol.EventAccept, carolPV)): "You joined this DM.",
		ptr(ev(protocol.EventDismiss, alice)):  "Alice removed you from this DM.",
	} {
		guestSelf.peer = bob
		if e.Author.Person == alice.Person {
			guestSelf.peer = alice
		}
		if got := eventText(marshal(t, *e), guestSelf); got != want {
			t.Fatalf("own guest: %q, want %q", got, want)
		}
	}

	// Another guest's lone shared invitation: inert, unnamed, no row.
	inert := carol
	inert.State, inert.Decision = client.PartInvited, ""
	other := dmPeople{me: dana, peer: bob, role: "visitor", humans: map[string]client.ParticipationInfo{pid: inert}}
	invite := marshal(t, ev(protocol.EventInvite, bob))
	if got := eventText(invite, other); strings.Contains(got, "Carol") || strings.Contains(got, "carol") || strings.Contains(got, "invited") {
		t.Fatalf("inert invitation exposed: %q", got)
	}
	seen := map[string]bool{}
	if other.eventShown(client.ConvMessage{Dir: "in", Body: invite}, seen) {
		t.Fatal("inert invitation row shown")
	}
	other.humans[pid] = carol
	if got := eventText(invite, other); got != "Bob invited Carol into this DM." {
		t.Fatalf("accepted guest hidden: %q", got)
	}
	if !other.eventShown(client.ConvMessage{Dir: "in", Body: invite}, seen) || other.eventShown(client.ConvMessage{Dir: "in", Body: invite}, seen) {
		t.Fatal("one row per signed event")
	}
	// An original's onward copy of Carol's acceptance is not a new row.
	accept := marshal(t, ev(protocol.EventAccept, carolPV))
	if member.eventShown(client.ConvMessage{Dir: "out", Body: accept}, map[string]bool{}) {
		t.Fatal("onward copy shown as a row")
	}
	if !member.eventShown(client.ConvMessage{Dir: "out", Body: marshal(t, ev(protocol.EventDismiss, alice))}, map[string]bool{}) {
		t.Fatal("own end hidden")
	}

	// Agent records keep their wording.
	agent := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("1", 64), PID: strings.Repeat("7", 32), Type: protocol.EventInvite, TS: 1790000000,
		Author: protocol.EventAuthor{Person: bob.Person, Roster: strings.Repeat("5", 64), Address: bob.Address, Fingerprint: fp}, Host: &protocol.ParticipationHost{Person: alice.Person, Address: alice.Address, Fingerprint: fp}}
	agent.Audience = protocol.AudienceConversation
	agent.Sign(key)
	if got := eventText(marshal(t, agent), member); got != "Bob invited your agent (on alice/laptop) into this DM." {
		t.Fatalf("agent wording changed: %q", got)
	}
	if !member.eventShown(client.ConvMessage{Dir: "out", Body: marshal(t, agent)}, map[string]bool{}) {
		t.Fatal("agent record hidden")
	}
}

func ptr(e protocol.ParticipationEvent) *protocol.ParticipationEvent { return &e }
