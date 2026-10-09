package envelope

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestRequestFollowupSignedShape(t *testing.T) {
	alice, bob := newParty(t, "alice/phone"), newParty(t, "bob/desk")
	ref := &Ref{ID: protocol.NewID(), Fingerprint: alice.pub.Fingerprint()}
	in := Inner{V: Version, ID: protocol.NewID(), From: alice.pub.Address, To: bob.pub.Address, TS: 1, Kind: KindTask, Body: "Please write the document in English.", ReplyTo: ref.ID, Followup: ref}
	recipient, _ := bob.pub.Recipient()
	sealed, err := Seal(in, alice.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Open(sealed, bob.id, bob.pub.Address, alice.pub)
	if err != nil || got.Followup == nil || *got.Followup != *ref {
		t.Fatalf("signed follow-up was lost: %+v %v", got.Followup, err)
	}
	for _, change := range []func(*Inner){
		func(n *Inner) { n.Kind = KindMessage },
		func(n *Inner) { n.Kind, n.Status = KindAnswer, StatusDone },
		func(n *Inner) { n.ReplyTo = "" },
		func(n *Inner) { n.Followup = &Ref{ID: n.ID, Fingerprint: ref.Fingerprint} },
		func(n *Inner) { n.Followup = &Ref{ID: ref.ID, Fingerprint: "unknown"} },
		func(n *Inner) { n.Origin = OriginAgentPrefix + "codex" },
	} {
		bad := in
		change(&bad)
		if err := CheckFollowup(bad); err == nil {
			t.Fatalf("invalid follow-up shape passed: %+v", bad)
		}
	}
	in.Followup, in.Quote = nil, ref.ID
	raw, _ := json.Marshal(in)
	if strings.Contains(string(raw), "followup") || CheckFollowup(in) != nil {
		t.Fatal("ordinary quotation acquired follow-up semantics")
	}
}
