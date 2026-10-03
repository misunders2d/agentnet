package envelope

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestProgressStatusShape(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	recipient, _ := bob.pub.Recipient()
	base := Inner{ID: strings.Repeat("1", 32), From: alice.pub.Address, To: bob.pub.Address, TS: 1,
		Kind: KindMessage, Body: "accepted; checking", ReplyTo: strings.Repeat("2", 32), Status: StatusProgress}
	env, err := Seal(base, alice.id.Sign, recipient)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(env, bob.id, bob.pub.Address, alice.pub); err != nil || got.Status != StatusProgress || got.ReplyTo != base.ReplyTo {
		t.Fatalf("Open = %+v, %v", got, err)
	}

	cases := map[string]func(*Inner){
		"version 2": func(in *Inner) {
			in.V, in.Conv, in.LID, in.Root = Version2, strings.Repeat("3", 64), strings.Repeat("4", 32), []byte(`{}`)
		},
		"not a message":   func(in *Inner) { in.Kind = KindAnswer },
		"no request":      func(in *Inner) { in.ReplyTo = "" },
		"empty text":      func(in *Inner) { in.Body = " \n" },
		"with attachment": func(in *Inner) { in.Attachments = []Attachment{{Name: "x"}} },
		"with target":     func(in *Inner) { in.Target = &Target{Address: "bob/b", AgentID: strings.Repeat("5", 32)} },
	}
	named := base
	named.AgentID = strings.Repeat("5", 32)
	if _, err := Seal(named, alice.id.Sign, recipient); err != nil {
		t.Fatalf("named executor progress: %v", err)
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			in := base
			change(&in)
			if _, err := Seal(in, alice.id.Sign, recipient); err == nil || !strings.Contains(err.Error(), "progress is") {
				t.Fatalf("Seal error = %v", err)
			}
		})
	}
}

// Progress shape vectors shared with the browser: each inner as JSON and
// whether checkVersion2 accepts it. AGENTNET_PROGRESS_VECTORS=PATH writes them.
func TestProgressShapeVectors(t *testing.T) {
	id := func(c string) string { return strings.Repeat(c, 32) }
	v1 := Inner{V: Version, ID: id("1"), From: "alice/a", To: "bob/b", TS: 1790000000, Kind: KindMessage, Body: "checking", ReplyTo: id("2"), Status: StatusProgress}
	v2 := Inner{V: Version2, ID: id("1"), From: "alice/a", To: "bob/b", TS: 1790000000, Kind: KindMessage, Body: "checking", ReplyTo: id("2"), Status: StatusProgress,
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
		{"answer kind", with(v2, func(in *Inner) { in.Kind = KindAnswer }), false},
		{"no request", with(v2, func(in *Inner) { in.ReplyTo = "" }), false},
		{"blank text", with(v2, func(in *Inner) { in.Body = " " }), false},
		{"attachment", with(v2, func(in *Inner) { in.Attachments = []Attachment{{Name: "x"}} }), false},
		{"target", with(v1, func(in *Inner) { in.Target = &Target{Address: "bob/b", AgentID: id("5")} }), false},
		{"receiver route", with(v1, func(in *Inner) { in.ReceiverRoute = &ReceiverRoute{Op: "request"} }), false},
		{"sub", with(v2, func(in *Inner) { in.Sub = SubEvent }), false},
		{"named author on a plain message", with(v1, func(in *Inner) { in.Status, in.AgentID = "", id("5") }), false},
	}
	for _, v := range vectors {
		if err := checkVersion2(v.Inner); (err == nil) != v.Valid {
			t.Errorf("%s: valid=%v, error %v", v.Name, v.Valid, err)
		}
	}
	if path := os.Getenv("AGENTNET_PROGRESS_VECTORS"); path != "" {
		data, _ := json.MarshalIndent(vectors, "", "  ")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
