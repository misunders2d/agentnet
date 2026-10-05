package ui

import (
	"crypto/ed25519"
	"github.com/misunders2d/agentnet/internal/protocol"
	"strings"
	"testing"
)

func TestGroupParticipationEventLabelsPreserveVisitorScope(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	e := protocol.ParticipationEvent{V: 1, Conv: strings.Repeat("1", 64), PID: strings.Repeat("2", 32), Type: protocol.EventAccept, Prev: strings.Repeat("3", 64), TS: 1790000000, Author: protocol.EventAuthor{Person: strings.Repeat("4", 32), Roster: strings.Repeat("5", 64), Address: "dana/desk", Fingerprint: "01234567-89abcdef-01234567-89abcdef"}}
	e.Sign(key)
	p := dmPeople{group: true}
	if got := eventText(marshal(t, e), p); got != "An outside agent's owner accepted: this outside agent now receives every new message and file in this group until removed." {
		t.Fatal(got)
	}
	// The unchanged DM projection remains separate from group participation.
	if got := eventText(marshal(t, e), dmPeople{}); got != "Someone not in this DM accepted: the agent joins this DM." {
		t.Fatal(got)
	}
	e.Author.GroupAdmission = strings.Repeat("a", 64)
	e.Sign(key)
	if got := eventText(marshal(t, e), p); got != "An outside agent's owner accepted: the agent participates in this group." {
		t.Fatal(got)
	}
	p.members = []PersonView{{Person: e.Author.Person, Label: "Dana"}}
	if got := eventText(marshal(t, e), p); got != "Dana accepted: the agent participates in this group." {
		t.Fatal(got)
	}
	e.Type, e.Prev = protocol.EventInvite, ""
	e.Host = &protocol.ParticipationHost{Person: strings.Repeat("6", 32), Address: "outside/desk", Fingerprint: e.Author.Fingerprint, AgentID: strings.Repeat("7", 32)}
	e.Audience = protocol.AudienceConversation
	e.Group = &protocol.ParticipationGroup{Seq: 1, Hash: strings.Repeat("b", 64), HostRole: "visitor"}
	e.Sign(key)
	if got := eventText(marshal(t, e), p); got != "Dana invited an unknown person's agent, whose owner is outside this group into this group." {
		t.Fatal(got)
	}
}
