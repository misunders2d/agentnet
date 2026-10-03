package ui

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"strings"
	"testing"
)

// BUG-40b: a guest or visitor holds both original members; an author who is
// neither is not "a DM member".
func TestEventTextVisitorNeverCallsAnOutsiderAMember(t *testing.T) {
	person := func(label, address string) PersonView {
		return PersonView{Person: protocol.NewID(), Label: label, Address: address, Fingerprint: "11111111-22222222-33333333-44444444"}
	}
	alice, bob, carol, dave := person("Alice", "alice/laptop"), person("Bob", "bob/desk"), person("Carol", "carol/box"), person("Dave", "dave/host")
	accept := func(by PersonView) string {
		ev := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("a", 64), PID: protocol.NewID(), Type: protocol.EventAccept, Prev: strings.Repeat("b", 64), TS: 1,
			Author: protocol.EventAuthor{Person: by.Person, Roster: strings.Repeat("c", 64), Address: by.Address, Fingerprint: by.Fingerprint}}
		raw, _ := json.Marshal(ev)
		return string(raw)
	}
	guest := dmPeople{me: carol, peer: alice, role: "visitor", members: []PersonView{alice, bob}}
	if got := eventText(accept(dave), guest); got != "Someone not in this DM accepted: the agent joins this DM." {
		t.Fatalf("an outside host's acceptance, seen by a guest: %q", got)
	}
	if got := eventText(accept(bob), guest); got != "Bob accepted: the agent joins this DM." {
		t.Fatalf("a member's acceptance, seen by a guest: %q", got)
	}
	// Only one original held here: nobody can tell whether the author is the other.
	guest.members = []PersonView{alice}
	if got := eventText(accept(dave), guest); got != "A DM member accepted: the agent joins this DM." {
		t.Fatalf("an author not told apart from a member: %q", got)
	}
}

// BUG-40b review: a person pinned here but outside this DM is named by the
// label they chose, which nothing verifies; the timeline marks that name, so
// an outsider labelled like a member never reads as that member. A member,
// and a person who is an accepted guest here now, are not marked.
func TestEventTextMarksAKnownOutsider(t *testing.T) {
	person := func(label, address string) PersonView {
		return PersonView{Person: protocol.NewID(), Label: label, Address: address, Fingerprint: "11111111-22222222-33333333-44444444"}
	}
	alice, bob, carol, outsider := person("Alice", "alice/laptop"), person("Bob", "bob/desk"), person("Carol", "carol/box"), person("Alice", "mallory/box")
	accept := func(by PersonView) string {
		ev := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("a", 64), PID: protocol.NewID(), Type: protocol.EventAccept, Prev: strings.Repeat("b", 64), TS: 1,
			Author: protocol.EventAuthor{Person: by.Person, Roster: strings.Repeat("c", 64), Address: by.Address, Fingerprint: by.Fingerprint}}
		raw, _ := json.Marshal(ev)
		return string(raw)
	}
	known := map[string]PersonView{alice.Person: alice, carol.Person: carol, outsider.Person: outsider}
	member := dmPeople{me: bob, peer: alice, role: "member", known: known}
	guest := dmPeople{me: carol, peer: alice, role: "visitor", members: []PersonView{alice, bob}, known: map[string]PersonView{bob.Person: bob, outsider.Person: outsider}}
	for _, p := range []dmPeople{member, guest} {
		if got := eventText(accept(outsider), p); got != "Alice (not in this DM) accepted: the agent joins this DM." {
			t.Fatalf("an outsider labelled like a member, seen by %s: %q", p.role, got)
		}
		if got := p.whose(outsider.Person); got != "Alice (not in this DM)'s" {
			t.Fatalf("an outsider's agent, seen by %s: %q", p.role, got)
		}
		if got := eventText(accept(alice), p); got != "Alice accepted: the agent joins this DM." {
			t.Fatalf("a member's acceptance, seen by %s: %q", p.role, got)
		}
		if got := p.whose(alice.Person); got != "Alice's" {
			t.Fatalf("a member's agent, seen by %s: %q", p.role, got)
		}
	}
	if got := member.who(carol.Person); got != "Carol (not in this DM)" {
		t.Fatalf("a pinned person who is no guest here: %q", got)
	}
	member.humans = map[string]client.ParticipationInfo{"guest": {PID: "guest", Role: protocol.RoleHuman, State: client.PartActive, Host: client.PersonInfo{Person: carol.Person, Label: "Carol"}}}
	if got := member.who(carol.Person); got != "Carol" {
		t.Fatalf("an accepted guest marked as not in this DM: %q", got)
	}
}

func TestHumanGuestProjectionTruthAndPositiveAuthority(t *testing.T) {
	p := client.ParticipationInfo{PID: "guest", Role: protocol.RoleHuman, State: client.PartInvited, HostHere: true}
	v := guestView(p, false, guestEnd{})
	if !v.CanDecide || v.CanSend || v.CanEnd || v.CanLeave {
		t.Fatal("pending guest acquired non-consent authority")
	}
	p.State = client.PartActive
	v = guestView(p, false, guestEnd{})
	if !v.CanSend || !v.CanLeave || v.CanEnd || v.CanDecide {
		t.Fatal("accepted exact host projection")
	}
	p.HostHere = false
	v = guestView(p, false, guestEnd{})
	if v.CanSend || v.CanLeave || v.CanEnd || v.CanDecide {
		t.Fatal("non-member or linked guest device acquired authority")
	}
	v = guestView(p, true, guestEnd{})
	if !v.CanSend || !v.CanEnd {
		t.Fatal("positive original member projection")
	}
	p.Held = 1
	v = guestView(p, true, guestEnd{})
	if v.CanSend || !v.AudiencePending || !strings.Contains(v.StateText, "verified") {
		t.Fatal("held proof represented as active")
	}
	p.Held = 0
	p.State = client.PartDismissed
	v = guestView(p, true, guestEnd{unheard: true})
	if v.CanSend || v.CanEnd || !v.AudiencePending || !strings.Contains(v.StateText, "Previously shared copies remain") || !strings.Contains(v.StateText, "Until every device in this DM has stored the end") {
		t.Fatal("ended state claims global recall or grants sends")
	}
	// Once the guest's device holds the end, or the guest left, nothing new
	// can reach them: nothing is pending, and shared copies still remain.
	for _, end := range []guestEnd{{}, {left: true}} {
		v = guestView(p, true, end)
		if v.CanSend || v.CanEnd || v.AudiencePending || !strings.Contains(v.StateText, "Previously shared copies remain") {
			t.Fatalf("settled end %+v: %+v", end, v)
		}
	}
	if v = guestView(p, true, guestEnd{left: true}); !strings.HasPrefix(v.StateText, "Left") {
		t.Fatalf("a guest who left shown as ended by someone: %q", v.StateText)
	}
}
