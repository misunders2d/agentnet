package envelope

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// A proposal (MEL-521) is an answer whose whole body is one plain-text
// task, replying to one request: version 1, or a participation's output.
func TestProposalStatusShape(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	recipient, _ := bob.pub.Recipient()
	base := Inner{ID: strings.Repeat("1", 32), From: alice.pub.Address, To: bob.pub.Address, TS: 1,
		Kind: KindAnswer, Body: "Edit CHANGELOG.md: add the 0.8.0 entry", ReplyTo: strings.Repeat("2", 32), Status: StatusProposal}
	env, err := Seal(base, alice.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(env, bob.id, bob.pub.Address, alice.pub); err != nil || got.Status != StatusProposal || got.Body != base.Body {
		t.Fatalf("Open = %+v, %v", got, err)
	}
	for name, change := range map[string]func(*Inner){
		"a result":        func(in *Inner) { in.Kind = KindResult },
		"a message":       func(in *Inner) { in.Kind = KindMessage },
		"no request":      func(in *Inner) { in.ReplyTo = "" },
		"blank text":      func(in *Inner) { in.Body = " \n" },
		"with attachment": func(in *Inner) { in.Attachments = []Attachment{{Name: "x"}} },
	} {
		t.Run(name, func(t *testing.T) {
			in := base
			change(&in)
			if _, err := Seal(in, alice.id.Sign, recipient); err == nil || !strings.Contains(err.Error(), "a proposal is") {
				t.Fatalf("Seal error = %v", err)
			}
		})
	}
}

// Proposal shape vectors shared with the browser: each inner as JSON and
// whether checkVersion2 accepts it. AGENTNET_PROPOSAL_VECTORS=PATH writes them.
func TestProposalShapeVectors(t *testing.T) {
	id := func(c string) string { return strings.Repeat(c, 32) }
	v1 := Inner{V: Version, ID: id("1"), From: "alice/a", To: "bob/b", TS: 1790000000, Kind: KindAnswer, Body: "update the changelog", ReplyTo: id("2"), Status: StatusProposal}
	v2 := Inner{V: Version2, ID: id("1"), From: "alice/a", To: "bob/b", TS: 1790000000, Kind: KindAnswer, Body: "update the changelog", ReplyTo: id("2"), Status: StatusProposal,
		Conv: testConv, LID: testLID, Root: json.RawMessage(`{"v":1}`), Origin: OriginAgentPrefix + "claude", Emotion: "neutral", PID: id("6")}
	with := func(in Inner, change func(*Inner)) Inner { change(&in); return in }
	type vector struct {
		Name  string `json:"name"`
		Inner Inner  `json:"inner"`
		Valid bool   `json:"valid"`
	}
	vectors := []vector{
		{"v1 default responder", v1, true},
		{"v1 named executor", with(v1, func(in *Inner) { in.AgentID = id("5") }), true},
		{"v2 participation", v2, true},
		{"v2 named participation agent", with(v2, func(in *Inner) { in.AgentID = id("5") }), true},
		{"v2 without participation", with(v2, func(in *Inner) { in.PID = "" }), false},
		{"v3", with(v2, func(in *Inner) { in.V = Version3 }), false},
		{"result kind", with(v1, func(in *Inner) { in.Kind = KindResult }), false},
		{"message kind", with(v1, func(in *Inner) { in.Kind = KindMessage }), false},
		{"no request", with(v2, func(in *Inner) { in.ReplyTo = "" }), false},
		{"blank text", with(v2, func(in *Inner) { in.Body = " " }), false},
		{"attachment", with(v2, func(in *Inner) { in.Attachments = []Attachment{{Name: "x"}} }), false},
		{"target", with(v1, func(in *Inner) { in.Target = &Target{Address: "bob/b", AgentID: id("5")} }), false},
		{"receiver route", with(v1, func(in *Inner) { in.ReceiverRoute = &ReceiverRoute{Op: "request"} }), false},
		{"sub", with(v2, func(in *Inner) { in.Sub = SubEvent }), false},
	}
	for _, v := range vectors {
		if err := checkVersion2(v.Inner); (err == nil) != v.Valid {
			t.Errorf("%s: valid=%v, error %v", v.Name, v.Valid, err)
		}
	}
	if path := os.Getenv("AGENTNET_PROPOSAL_VECTORS"); path != "" {
		data, _ := json.MarshalIndent(vectors, "", "  ")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
