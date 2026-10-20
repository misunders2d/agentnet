package envelope

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestDirectTopicSyncQuietCarrier(t *testing.T) {
	a, b := newParty(t, "admin/laptop"), newParty(t, "admin/phone")
	raw, _ := json.Marshal(protocol.DirectTopicSync{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Placements: []protocol.DirectTopicPlacement{{Peer: "bob/desk", ID: protocol.NewID(), Author: a.pub.Fingerprint(), Topic: protocol.NewID(), Revision: 1, Writer: a.pub.Fingerprint(), Synthetic: true, Destination: protocol.DirectTopicDestination{Host: "bob/desk", Fingerprint: a.pub.Fingerprint()}}}})
	in := Inner{V: Version2, ID: protocol.NewID(), From: a.pub.Address, To: b.pub.Address, TS: time.Now().Unix(), Kind: KindMessage, Sub: SubDirectTopicSync, Replica: true, Body: string(raw)}
	recipient, _ := b.pub.Recipient()
	env, err := Seal(in, a.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if env.Attn {
		t.Fatal("placement notifies")
	}
	if _, err = Open(env, b.id, b.pub.Address, a.pub); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Inner){func(i *Inner) { i.Kind = KindTask }, func(i *Inner) { i.Kind = KindQuestion }, func(i *Inner) { i.Replica = false }, func(i *Inner) { i.Topic = protocol.NewID() }, func(i *Inner) { i.ReplyTo = protocol.NewID() }, func(i *Inner) { i.Conv = strings.Repeat("c", 64) }, func(i *Inner) { i.AgentID = protocol.NewID() }, func(i *Inner) { i.SendGroup = protocol.NewID() }, func(i *Inner) { i.Sub = SubTopicStateSync }} {
		bad := in
		mutate(&bad)
		if _, err = Seal(bad, a.id.Sign, recipient); err == nil {
			t.Fatal("placement accepted active message fields or another subtype")
		}
	}
	if _, err = SealAttention(in, a.id.Sign, recipient, protocol.NotifyChannel(strings.Repeat("b", 64), b.pub.Fingerprint())); err == nil {
		t.Fatal("placement accepted attention")
	}
}
