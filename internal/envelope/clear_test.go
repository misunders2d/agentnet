package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

// A conversation deletion is a conversation control only: its payload names
// one deletion, its part and exact turns, all bounded; anything else (a cut,
// anchors, an unknown field) is refused before it is stored.
func TestClearPayload(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	turn := Ref{ID: testLID, Fingerprint: alice.pub.Fingerprint()}
	clear := func(c Clear) Inner {
		body, _ := json.Marshal(c)
		in := v3Inner(alice, bob, SubClear, string(body))
		in.Conv, in.LID, in.Replica = testConv, "00112233445566778899aabbccddee00", true
		return in
	}
	ok := Clear{Deletion: testLID, Part: 1, Parts: 2, Turns: []Ref{turn}}
	env, err := Seal(clear(ok), alice.id.Sign, r)
	if err != nil {
		t.Fatalf("a valid deletion: %v", err)
	}
	got, err := Open(env, bob.id, bob.pub.Address, alice.pub)
	if err != nil || got.Sub != SubClear || got.Conv != testConv {
		t.Fatalf("open: %+v %v", got, err)
	}
	many := make([]Ref, MaxClearTurns+1)
	for i := range many {
		many[i] = turn
	}
	bad := map[string]Inner{
		"a device thread":       func() Inner { in := clear(ok); in.Conv, in.LID, in.Replica = "", "", false; return in }(),
		"a cut": func() Inner {
			in := clear(ok)
			in.Body = strings.TrimSuffix(in.Body, "}") + `,"cut":true,"anchors":[]}`
			return in
		}(),
		"part beyond parts": clear(Clear{Deletion: testLID, Part: 3, Parts: 2}),
		"no part":               clear(Clear{Deletion: testLID, Parts: 1}),
		"too many parts":        clear(Clear{Deletion: testLID, Part: 1, Parts: MaxClearParts + 1}),
		"too many turns":        clear(Clear{Deletion: testLID, Part: 1, Parts: 1, Turns: many}),
		"a bad turn":            clear(Clear{Deletion: testLID, Part: 1, Parts: 1, Turns: []Ref{{ID: "x", Fingerprint: turn.Fingerprint}}}),
		"no deletion":           clear(Clear{Part: 1, Parts: 1}),
		"an unknown field": func() Inner {
			in := clear(ok)
			in.Body = strings.TrimSuffix(in.Body, "}") + `,"all":true}`
			return in
		}(),
	}
	for name, in := range bad {
		if _, err := Seal(in, alice.id.Sign, r); err == nil {
			t.Errorf("%s: sealed", name)
		}
	}
}
