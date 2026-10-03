package envelope

import (
	"encoding/json"
	"strings"
	"testing"
)

func v3Inner(from, to party, sub, body string) Inner {
	return Inner{V: Version3, ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", From: from.pub.Address, To: to.pub.Address, TS: 1700000000,
		Kind: KindMessage, Sub: sub, Body: body, Ref: &Ref{ID: testLID, Fingerprint: from.pub.Fingerprint()}}
}

// A control round-trips in both scopes, signed in its own domain; a plain
// Seal of an unknown version is refused, never sent as version 1.
func TestVersion3RoundTripAndDomain(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	in := v3Inner(alice, bob, SubReaction, `{"emoji":"👍","op":"add","n":1}`)
	env, err := Seal(in, alice.id.Sign, r)
	if err != nil || env.V != Version3 {
		t.Fatalf("seal v3: %+v %v", env, err)
	}
	got, err := Open(env, bob.id, bob.pub.Address, alice.pub)
	if err != nil || got.Ref == nil || got.Ref.ID != testLID || got.Sub != SubReaction || got.Conv != "" {
		t.Fatalf("open v3: %+v %v", got, err)
	}
	conv := in
	conv.Conv, conv.LID, conv.Replica = testConv, "00112233445566778899aabbccddee00", true
	if _, err := Seal(conv, alice.id.Sign, r); err != nil {
		t.Fatalf("conversation-scoped control: %v", err)
	}
	as2 := env
	as2.V = Version2
	if as2.VerifySig(alice.pub.SignKey) == nil {
		t.Fatal("a version 3 signature verified as version 2")
	}
	as1 := env
	as1.V = Version
	if as1.VerifySig(alice.pub.SignKey) == nil {
		t.Fatal("a version 3 signature verified as version 1")
	}
	odd := in
	odd.V = 4
	if _, err := Seal(odd, alice.id.Sign, r); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("version 4 sealed: %v", err)
	}
	// A ref never rides on an older version.
	old := v2Inner(alice, bob, KindMessage)
	old.Ref = &Ref{ID: testLID, Fingerprint: alice.pub.Fingerprint()}
	if _, err := Seal(old, alice.id.Sign, r); err == nil {
		t.Fatal("a ref on version 2 was sealed")
	}
	// The receiver checks too, whoever built the envelope.
	forged := in
	forged.Target = &Target{Address: "bob/b", Fingerprint: bob.pub.Fingerprint()}
	forged.Kind = KindQuestion
	if _, err := Open(sealUnchecked(t, forged, Version3, alice.id.Sign, bob), bob.id, "bob/b", alice.pub); err == nil {
		t.Fatal("a control with an executor was opened")
	}
}

// Every field a control may not carry, and every malformed payload, is
// refused; the valid shapes pass.
func TestVersion3Strict(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	base := v3Inner(alice, bob, SubReaction, `{"emoji":"👍","op":"add","n":1}`)
	bad := map[string]func(*Inner){
		"no ref":                   func(in *Inner) { in.Ref = nil },
		"bad ref id":               func(in *Inner) { in.Ref = &Ref{ID: "x", Fingerprint: in.Ref.Fingerprint} },
		"blank fingerprint":        func(in *Inner) { in.Ref = &Ref{ID: in.Ref.ID} },
		"a question":               func(in *Inner) { in.Kind = KindQuestion },
		"a root":                   func(in *Inner) { in.Root = json.RawMessage(`{"v":1}`) },
		"a participation":          func(in *Inner) { in.PID = testLID },
		"a fan on a device thread": func(in *Inner) { in.Fan = []Fan{{Person: testLID, Roster: testConv}} },
		"a bad fan in a conv": func(in *Inner) {
			in.Conv, in.LID, in.Fan = testConv, "00112233445566778899aabbccddee01", []Fan{{Person: "x", Roster: testConv}}
		},
		"a reply":            func(in *Inner) { in.ReplyTo = testLID },
		"an origin":          func(in *Inner) { in.Origin = OriginUI },
		"a lid without conv": func(in *Inner) { in.LID = testLID },
		"a conv without lid": func(in *Inner) { in.Conv = testConv },
		"unknown sub":        func(in *Inner) { in.Sub = "vote" },
		"a turn":             func(in *Inner) { in.Sub = "" },
		"reaction: no emoji": func(in *Inner) { in.Body = `{"emoji":"","op":"add","n":1}` },
		"reaction: a word":   func(in *Inner) { in.Body = `{"emoji":"lol","op":"add","n":1}` },
		"reaction: bad op":   func(in *Inner) { in.Body = `{"emoji":"👍","op":"toggle","n":1}` },
		"reaction: extra":    func(in *Inner) { in.Body = `{"emoji":"👍","op":"add","n":1,"x":1}` },
		"reaction: trailing": func(in *Inner) { in.Body = `{"emoji":"👍","op":"add","n":1}{}` },
		"revision: empty":    func(in *Inner) { in.Sub, in.Body = SubRevision, `{"rev":1,"text":"  "}` },
		"revision: rev 0":    func(in *Inner) { in.Sub, in.Body = SubRevision, `{"rev":0,"text":"x"}` },
		"revision: oversized": func(in *Inner) {
			in.Sub, in.Body = SubRevision, `{"rev":1,"text":"`+strings.Repeat("a", MaxRevisionBytes+1)+`"}`
		},
		"retraction: long": func(in *Inner) {
			in.Sub, in.Body = SubRetraction, `{"reason":"`+strings.Repeat("r", MaxReasonBytes+1)+`"}`
		},
		"retraction: garbage":  func(in *Inner) { in.Sub, in.Body = SubRetraction, `nope` },
		"retraction: as react": func(in *Inner) { in.Sub, in.Body = SubRetraction, `{"emoji":"👍"}` },
	}
	for name, mut := range bad {
		in := base
		mut(&in)
		if _, err := Seal(in, alice.id.Sign, r); err == nil {
			t.Errorf("%s: sealed", name)
		}
	}
	good := map[string]func(*Inner){
		"flag emoji":     func(in *Inner) { in.Body = `{"emoji":"🇱🇻","op":"add","n":3}` },
		"skin tone":      func(in *Inner) { in.Body = `{"emoji":"👍🏽","op":"remove","n":2}` },
		"keycap":         func(in *Inner) { in.Body = `{"emoji":"1️⃣","op":"add","n":1}` },
		"zwj family":     func(in *Inner) { in.Body = `{"emoji":"👨‍👩‍👧","op":"add","n":1}` },
		"heart":          func(in *Inner) { in.Body = `{"emoji":"❤️","op":"add","n":1}` },
		"revision":       func(in *Inner) { in.Sub, in.Body = SubRevision, `{"rev":2,"text":"corrected"}` },
		"retraction":     func(in *Inner) { in.Sub, in.Body = SubRetraction, `{}` },
		"with a reason":  func(in *Inner) { in.Sub, in.Body = SubRetraction, `{"reason":"sent by mistake"}` },
		"in a conv":      func(in *Inner) { in.Conv, in.LID = testConv, "00112233445566778899aabbccddee01" },
		"conv replica":   func(in *Inner) { in.Conv, in.LID, in.Replica = testConv, "00112233445566778899aabbccddee01", true },
		"keycap digit 1": func(in *Inner) { in.Body = `{"emoji":"#️⃣","op":"add","n":1}` },
		"conv with fan": func(in *Inner) {
			in.Conv, in.LID, in.Fan = testConv, "00112233445566778899aabbccddee01", []Fan{{Person: testLID, Roster: testConv}, {Person: "ffeeddccbbaa99887766554433221100", Roster: testConv}}
		},
	}
	for name, mut := range good {
		in := base
		mut(&in)
		if _, err := Seal(in, alice.id.Sign, r); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestValidEmoji(t *testing.T) {
	for _, ok := range []string{"👍", "❤️", "🇱🇻", "👍🏽", "1️⃣", "#️⃣", "👨‍👩‍👧‍👦", "🏴󠁧󠁢󠁳󠁣󠁴󠁿", "✓", "☕", "★"} {
		if !ValidEmoji(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "a", "lol", "1", " ", "👍 ", "👍a", "‍", "️", strings.Repeat("👍", MaxEmojiRunes+1), "\xff", "<b>", "-"} {
		if ValidEmoji(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A reaction composed here is one emoji; what peers already sent within
// ValidEmoji stays readable.
func TestOneEmoji(t *testing.T) {
	for _, ok := range []string{"👍", "❤️", "🇱🇻", "👍🏽", "1️⃣", "#️⃣", "👨‍👩‍👧‍👦", "🏴󠁧󠁢󠁳󠁣󠁴󠁿", "✓", "☕", "★", "©️", "↔️", "🫩"} {
		if !OneEmoji(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"$", "+", "€", "^", "×", "𠀀", "👍👍", strings.Repeat("👍", MaxEmojiRunes), "🇱🇻🇺🇸", "🇱🇻👍", "11⃣", "\u200d👍", "👍\u200d", "👍\u200d$", "a", ""} {
		if OneEmoji(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, sent := range []string{"$", "+", "𠀀", strings.Repeat("👍", MaxEmojiRunes)} {
		if !ValidEmoji(sent) {
			t.Errorf("%q, as peers sent it, is no longer readable", sent)
		}
	}
}
