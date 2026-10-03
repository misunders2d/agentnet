package envelope

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Assistant reaction shape vectors shared with the browser: each inner as
// JSON and whether checkVersion2 accepts it. AGENTNET_REACTION_VECTORS=PATH
// writes them.
func TestAssistantReactionShapeVectors(t *testing.T) {
	id := func(c string) string { return strings.Repeat(c, 32) }
	const fp = "01234567-89abcdef-01234567-89abcdef"
	thread := Inner{V: Version3, ID: id("1"), From: "bob/b", To: "alice/a", TS: 1790000000, Kind: KindMessage, Sub: SubReaction,
		Body: `{"emoji":"👍","op":"add","n":1}`, Ref: &Ref{ID: id("2"), Fingerprint: fp}}
	conv := thread
	conv.Conv, conv.LID = testConv, testLID
	with := func(in Inner, change func(*Inner)) Inner { change(&in); return in }
	type vector struct {
		Name  string `json:"name"`
		Inner Inner  `json:"inner"`
		Valid bool   `json:"valid"`
	}
	vectors := []vector{
		{"human device thread", thread, true},
		{"human conversation", conv, true},
		{"assistant device thread", with(thread, func(in *Inner) { in.AgentID = id("5") }), true},
		{"assistant conversation participation", with(conv, func(in *Inner) { in.PID = id("6") }), true},
		{"named assistant conversation participation", with(conv, func(in *Inner) { in.PID, in.AgentID = id("6"), id("5") }), true},
		{"device thread with pid", with(thread, func(in *Inner) { in.PID, in.AgentID = id("6"), id("5") }), false},
		{"conversation agent without pid", with(conv, func(in *Inner) { in.AgentID = id("5") }), false},
		{"invalid agent id", with(thread, func(in *Inner) { in.AgentID = "agent" }), false},
		{"revision with agent", with(thread, func(in *Inner) { in.Sub, in.Body, in.AgentID = SubRevision, `{"rev":1,"text":"x"}`, id("5") }), false},
		{"retraction with pid", with(conv, func(in *Inner) { in.Sub, in.Body, in.PID = SubRetraction, `{}`, id("6") }), false},
		{"assistant reaction with origin", with(conv, func(in *Inner) { in.PID, in.Origin = id("6"), OriginAgentPrefix+"claude" }), false},
		{"assistant reaction with target", with(thread, func(in *Inner) { in.AgentID, in.Target = id("5"), &Target{Address: "bob/b", AgentID: id("5")} }), false},
		{"default responder device thread", with(thread, func(in *Inner) { in.Origin = OriginAgentPrefix + "claude" }), true},
		{"default responder bare agent origin", with(thread, func(in *Inner) { in.Origin = OriginAgentPrefix }), false},
		{"default responder bad harness token", with(thread, func(in *Inner) { in.Origin = OriginAgentPrefix + "Claude Code" }), false},
		{"named executor with origin", with(thread, func(in *Inner) { in.AgentID, in.Origin = id("5"), OriginAgentPrefix+"claude" }), false},
		{"default responder in conversation", with(conv, func(in *Inner) { in.Origin = OriginAgentPrefix + "claude" }), false},
		{"human origin on a reaction", with(thread, func(in *Inner) { in.Origin = OriginUI }), false},
		{"revision with agent origin", with(thread, func(in *Inner) {
			in.Sub, in.Body, in.Origin = SubRevision, `{"rev":1,"text":"x"}`, OriginAgentPrefix+"claude"
		}), false},
	}
	for _, v := range vectors {
		if err := checkVersion2(v.Inner); (err == nil) != v.Valid {
			t.Errorf("%s: valid=%v, error %v", v.Name, v.Valid, err)
		}
	}
	if path := os.Getenv("AGENTNET_REACTION_VECTORS"); path != "" {
		data, _ := json.MarshalIndent(vectors, "", "  ")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
