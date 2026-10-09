package envelope

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestModelSyncQuietOwnMetadataShape(t *testing.T) {
	raw, _ := json.Marshal(protocol.ModelSync{V: 1, Person: protocol.NewID(), Roster: strings.Repeat("a", 64), Reports: []protocol.AgentModel{{Model: "gpt-6.1-sol", Harness: "codex", Executor: strings.Repeat("b", 64), At: 1, Revision: 1}}})
	in := Inner{V: Version2, ID: protocol.NewID(), From: "owner/host", To: "owner/phone", TS: 1, Kind: KindMessage, Sub: SubModelSync, Replica: true, Body: string(raw)}
	if err := checkVersion2(in); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Inner){"session": func(n *Inner) { n.Session = protocol.NewID() }, "fallback": func(n *Inner) { n.Fallback = true }, "send group": func(n *Inner) { n.SendGroup = protocol.NewID() }, "kind": func(n *Inner) { n.Kind = KindTask }, "target": func(n *Inner) { n.Target = &Target{Address: n.To} }, "agent": func(n *Inner) { n.AgentID = protocol.NewID() }, "reply": func(n *Inner) { n.ReplyTo = protocol.NewID() }, "origin": func(n *Inner) { n.Origin = "ui" }, "status": func(n *Inner) { n.Status = StatusDone }, "replica": func(n *Inner) { n.Replica = false }, "topic": func(n *Inner) { n.Topic = protocol.NewID() }, "pid": func(n *Inner) { n.PID = protocol.NewID() }, "fan": func(n *Inner) { n.Fan = []Fan{} }} {
		t.Run(name, func(t *testing.T) {
			n := in
			change(&n)
			if checkVersion2(n) == nil {
				t.Fatal("model metadata accepted another message's fields")
			}
		})
	}
	from, to := newParty(t, in.From), newParty(t, in.To)
	recipient, _ := to.pub.Recipient()
	if _, err := SealAttention(in, from.id.Sign, recipient, protocol.NewID()); err == nil {
		t.Fatal("model metadata requests attention")
	}
}
