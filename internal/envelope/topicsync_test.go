package envelope

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTopicSyncQuietCarrier(t *testing.T) {
	a, b := newParty(t, "admin/laptop"), newParty(t, "admin/phone")
	raw, _ := json.Marshal(protocol.TopicSync{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Titles: []protocol.TopicTitle{{Scope: strings.Repeat("b", 64), Topic: protocol.NewID(), Title: "", Rev: 1, Writer: a.pub.Fingerprint()}}})
	in := Inner{V: Version2, ID: protocol.NewID(), From: a.pub.Address, To: b.pub.Address, TS: time.Now().Unix(), Kind: KindMessage, Sub: SubTopicSync, Replica: true, Body: string(raw)}
	r, _ := b.pub.Recipient()
	env, err := Seal(in, a.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if env.Attn {
		t.Fatal("private preference notifies")
	}
	if _, err = Open(env, b.id, b.pub.Address, a.pub); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Inner){
		func(i *Inner) { i.Kind = KindTask }, func(i *Inner) { i.Replica = false }, func(i *Inner) { i.Topic = protocol.NewID() }, func(i *Inner) { i.ReplyTo = protocol.NewID() }, func(i *Inner) { i.Conv = strings.Repeat("c", 64) }, func(i *Inner) { i.AgentID = protocol.NewID() }, func(i *Inner) { i.SendGroup = protocol.NewID() },
	} {
		bad := in
		mutate(&bad)
		if _, err = Seal(bad, a.id.Sign, r); err == nil {
			t.Fatal("topic preference accepted active message fields")
		}
	}
	if _, err = SealAttention(in, a.id.Sign, r, protocol.NotifyChannel(strings.Repeat("b", 64), b.pub.Fingerprint())); err == nil {
		t.Fatal("private preference accepted attention")
	}
}
