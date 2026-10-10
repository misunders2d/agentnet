package envelope

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestTopicStateSyncQuietCarrier(t *testing.T) {
	a, b := newParty(t, "admin/laptop"), newParty(t, "admin/phone")
	raw, _ := json.Marshal(protocol.TopicStateSync{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Marks: []protocol.TopicMark{{Scope: "bob/desk", Topic: protocol.NewID(), Mark: protocol.TopicMarkDone, Count: 2, At: time.Now().Unix(), Writer: a.pub.Fingerprint()}}})
	in := Inner{V: Version2, ID: protocol.NewID(), From: a.pub.Address, To: b.pub.Address, TS: time.Now().Unix(), Kind: KindMessage, Sub: SubTopicStateSync, Replica: true, Body: string(raw)}
	r, _ := b.pub.Recipient()
	env, err := Seal(in, a.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if env.Attn {
		t.Fatal("private mark notifies")
	}
	if _, err = Open(env, b.id, b.pub.Address, a.pub); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Inner){
		func(i *Inner) { i.Kind = KindTask }, func(i *Inner) { i.Kind = KindQuestion }, func(i *Inner) { i.Replica = false }, func(i *Inner) { i.Topic = protocol.NewID() }, func(i *Inner) { i.ReplyTo = protocol.NewID() }, func(i *Inner) { i.Conv = strings.Repeat("c", 64) }, func(i *Inner) { i.AgentID = protocol.NewID() }, func(i *Inner) { i.SendGroup = protocol.NewID() }, func(i *Inner) { i.TopicEvent = &TopicEvent{Action: "done"} },
		func(i *Inner) { i.Body = strings.Replace(i.Body, `"marks"`, `"titles"`, 1) }, func(i *Inner) { i.Sub = SubTopicSync },
	} {
		bad := in
		mutate(&bad)
		if _, err = Seal(bad, a.id.Sign, r); err == nil {
			t.Fatal("topic mark accepted active message fields or another body")
		}
	}
	if _, err = SealAttention(in, a.id.Sign, r, protocol.NotifyChannel(strings.Repeat("b", 64), b.pub.Fingerprint())); err == nil {
		t.Fatal("private mark accepted attention")
	}
}
