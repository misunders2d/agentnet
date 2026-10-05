package envelope

import (
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

func TestQuoteAndTopicDone(t *testing.T) {
	id, quote := protocol.NewID(), protocol.NewID()
	base := Inner{V: Version, ID: id, Kind: KindMessage, Quote: quote}
	if err := checkVersion2(base); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Inner){func(n *Inner) { n.Quote = n.ID }, func(n *Inner) { n.Kind = KindAnswer }, func(n *Inner) { n.Origin = "agent:codex" }, func(n *Inner) { n.V = Version3 }, func(n *Inner) { n.Status = StatusDone }} {
		n := base
		edit(&n)
		if checkVersion2(n) == nil {
			t.Fatalf("accepted %+v", n)
		}
	}
	base = Inner{V: Version, ID: id, Kind: KindAnswer, Status: StatusDone, TopicDone: true}
	if err := checkVersion2(base); err != nil {
		t.Fatal(err)
	}
	for _, edit := range []func(*Inner){func(n *Inner) { n.V = Version2 }, func(n *Inner) { n.Status = StatusProgress }, func(n *Inner) { n.Kind = KindMessage }} {
		n := base
		edit(&n)
		if checkVersion2(n) == nil {
			t.Fatalf("accepted %+v", n)
		}
	}
	a, b := newParty(t, "a/laptop"), newParty(t, "b/phone")
	recipient, _ := b.pub.Recipient()
	base.From = a.pub.Address
	base.To = b.pub.Address
	base.TS = 1
	base.Body = "finished"
	env, err := Seal(base, a.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(env, b.id, b.pub.Address, a.pub)
	if err != nil || !opened.TopicDone {
		t.Fatalf("roundtrip %+v %v", opened, err)
	}
}
