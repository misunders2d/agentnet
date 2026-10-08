package envelope

import (
	"github.com/misunders2d/agentnet/internal/protocol"
	"testing"
)

func TestSendGroupHumanRequestValidation(t *testing.T) {
	in := Inner{V: Version2, Conv: protocol.NewID(), LID: protocol.NewID(), ID: protocol.NewID(), PID: protocol.NewID(), Kind: KindQuestion, Origin: OriginUI, Target: &Target{Address: "bob/laptop"}, SendGroup: protocol.NewID()}
	if err := CheckSendGroup(in); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Inner){func(x *Inner) { x.SendGroup = "bad" }, func(x *Inner) { x.Origin = "agent:codex" }, func(x *Inner) { x.Target = nil }, func(x *Inner) { x.Kind = KindAnswer }, func(x *Inner) { x.Sub = SubHistory }, func(x *Inner) { x.PID = "" }} {
		bad := in
		change(&bad)
		if CheckSendGroup(bad) == nil {
			t.Fatalf("invalid grouping allowed: %+v", bad)
		}
	}
	old := in
	old.SendGroup = ""
	old.Kind = KindMessage
	old.Target = nil
	if err := CheckSendGroup(old); err != nil {
		t.Fatal("legacy ungrouped message rejected", err)
	}
}
