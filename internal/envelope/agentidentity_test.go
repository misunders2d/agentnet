package envelope

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNamedAgentDeviceEnvelope(t *testing.T) {
	a, b := newParty(t, "alice/a"), newParty(t, "bob/b")
	id := protocol.NewID()
	base := Inner{V: Version, ID: protocol.NewID(), From: a.pub.Address, To: b.pub.Address, TS: time.Now().Unix(), Kind: KindQuestion, Body: "question",
		Target: &Target{Address: b.pub.Address, Fingerprint: b.pub.Fingerprint(), AgentID: id}}
	r, err := b.pub.Recipient()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{KindQuestion, KindTask} {
		in := base
		in.Kind = kind
		env, err := Seal(in, a.id.Sign, r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Open(env, b.id, b.pub.Address, a.pub)
		if err != nil || got.Target == nil || *got.Target != *in.Target {
			t.Fatalf("round trip: %+v, %v", got, err)
		}
	}
	for _, kind := range []string{KindAnswer, KindResult} {
		in := base
		in.Kind, in.Target, in.AgentID, in.ReplyTo = kind, nil, id, protocol.NewID()
		env, err := Seal(in, a.id.Sign, r)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Open(env, b.id, b.pub.Address, a.pub)
		if err != nil || got.AgentID != id {
			t.Fatalf("author round trip: %+v, %v", got, err)
		}
	}
	cases := map[string]func(*Inner){
		"wrong recipient":      func(in *Inner) { in.Target.Address = "carol/c" },
		"missing agent":        func(in *Inner) { in.Target.AgentID = "" },
		"invalid agent":        func(in *Inner) { in.Target.AgentID = "program-name" },
		"missing host key":     func(in *Inner) { in.Target.Fingerprint = "" },
		"plain message target": func(in *Inner) { in.Kind = KindMessage },
		"request author":       func(in *Inner) { in.AgentID = id },
		"unlinked answer":      func(in *Inner) { in.Target = nil; in.Kind = KindAnswer; in.AgentID = id },
		"control author":       func(in *Inner) { in.V = Version3; in.Target = nil; in.AgentID = id },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			in := base
			target := *base.Target
			in.Target = &target
			change(&in)
			if _, err := Seal(in, a.id.Sign, r); err == nil {
				t.Fatal("invalid agent fields accepted")
			}
		})
	}
}

func TestAbsentAgentFieldsKeepLegacyJSON(t *testing.T) {
	target, err := json.Marshal(Target{Address: "alice/a", Fingerprint: strings.Repeat("a", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if string(target) != `{"address":"alice/a","fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}` {
		t.Fatalf("legacy target changed: %s", target)
	}
	in := Inner{V: Version, ID: "old", From: "alice/a", To: "bob/b", TS: 1, Kind: KindMessage, Body: "hello"}
	got, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"v":1,"id":"old","from":"alice/a","to":"bob/b","ts":1,"kind":"message","body":"hello"}` {
		t.Fatalf("legacy inner changed: %s", got)
	}
}
