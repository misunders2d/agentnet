package envelope

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

const (
	testConv = "e0758d3e1872da6abc62304e16423c9ae8782d39be3517e4503df4b6ac88b75a"
	testLID  = "00112233445566778899aabbccddeeff"
)

func v2Inner(from, to party, kind string) Inner {
	return Inner{V: Version2, ID: "m2", From: from.pub.Address, To: to.pub.Address, TS: time.Now().Unix(), Kind: kind, Body: "hi",
		Conv: testConv, LID: testLID, Root: json.RawMessage(`{"v":1}`), Origin: OriginUI}
}

// sealUnchecked is Seal without its checks, to build what a faulty or
// hostile sender could send.
func sealUnchecked(t *testing.T, in Inner, v int, sender ed25519.PrivateKey, to party) Envelope {
	t.Helper()
	r, _ := to.pub.Recipient()
	plain, _ := json.Marshal(in)
	var ct bytes.Buffer
	w, err := age.Encrypt(&ct, r)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(plain)
	w.Close()
	env := Envelope{V: v, ID: in.ID, From: in.From, To: in.To, TS: in.TS, Kind: in.Kind, CT: ct.Bytes()}
	env.Sig = ed25519.Sign(sender, env.signed())
	return env
}

// A version 2 message round-trips with its conversation fields, signed in
// its own domain; the Hub's VerifySig accepts it.
func TestVersion2RoundTrip(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	in := v2Inner(alice, bob, KindQuestion)
	in.Target = &Target{Address: "bob/b", Fingerprint: bob.pub.Fingerprint()}
	env, err := Seal(in, alice.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if env.V != Version2 || !strings.HasPrefix(string(env.signed()), "agentnet-envelope-v2\n") {
		t.Fatalf("version %d, domain %q", env.V, env.signed()[:21])
	}
	if err := env.VerifySig(alice.pub.SignKey); err != nil {
		t.Fatal(err)
	}
	got, err := Open(env, bob.id, bob.pub.Address, alice.pub)
	if err != nil || got.Conv != testConv || got.LID != testLID || got.Origin != OriginUI || got.Target == nil || got.Target.Address != "bob/b" {
		t.Fatalf("Open = %+v, %v", got, err)
	}
	// A plain Seal without V stays version 1, as before.
	if env := seal(t, alice, bob, "old"); env.V != Version {
		t.Fatalf("default version %d", env.V)
	}
}

// Signatures never cross versions: relabelling the outer version fails.
func TestVersionDomainsSeparate(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	env2, err := Seal(v2Inner(alice, bob, KindMessage), alice.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	as1 := env2
	as1.V = Version
	if as1.VerifySig(alice.pub.SignKey) == nil {
		t.Fatal("a version 2 signature verified as version 1")
	}
	env1 := seal(t, alice, bob, "v1")
	as2 := env1
	as2.V = Version2
	if as2.VerifySig(alice.pub.SignKey) == nil {
		t.Fatal("a version 1 signature verified as version 2")
	}
	odd := env1
	odd.V = 3
	if odd.VerifySig(alice.pub.SignKey) == nil {
		t.Fatal("version 3 accepted")
	}
}

// Conversation fields are refused in version 1, and version 2 fields are
// checked, whoever built the envelope.
func TestVersion2FieldsChecked(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	sneaky := Inner{ID: "m1", From: "alice/a", To: "bob/b", TS: 1, Kind: KindMessage, Body: "x", Conv: testConv}
	if _, err := Seal(sneaky, alice.id.Sign, r); err == nil {
		t.Fatal("Seal accepted conversation fields in version 1")
	}
	sneaky.V = Version
	if _, err := Open(sealUnchecked(t, sneaky, Version, alice.id.Sign, bob), bob.id, "bob/b", alice.pub); err == nil {
		t.Fatal("Open accepted conversation fields in version 1")
	}
	for name, mut := range map[string]func(*Inner){
		"no conversation":       func(in *Inner) { in.Conv = "" },
		"bad logical id":        func(in *Inner) { in.LID = "x" },
		"no root":               func(in *Inner) { in.Root = nil },
		"oversized root":        func(in *Inner) { in.Root = json.RawMessage(`"` + strings.Repeat("r", 3000) + `"`) },
		"unknown sub":           func(in *Inner) { in.Sub = "vote" },
		"bad origin":            func(in *Inner) { in.Origin = "keyboard" },
		"bad agent origin":      func(in *Inner) { in.Origin = "agent:Claude Code" },
		"bad emotion":           func(in *Inner) { in.Emotion = "Very Happy" },
		"target on a message":   func(in *Inner) { in.Target = &Target{Address: "bob/b", Fingerprint: bob.pub.Fingerprint()} },
		"target with a bad key": func(in *Inner) { in.Kind = KindTask; in.Target = &Target{Address: "bob/b", Fingerprint: "x"} },
	} {
		in := v2Inner(alice, bob, KindMessage)
		mut(&in)
		if _, err := Seal(in, alice.id.Sign, r); err == nil {
			t.Errorf("Seal accepted %s", name)
		}
		if _, err := Open(sealUnchecked(t, in, Version2, alice.id.Sign, bob), bob.id, "bob/b", alice.pub); err == nil {
			t.Errorf("Open accepted %s", name)
		}
	}
}

// An agent's turn must carry an emotion when sent; one received without is
// kept (shown as "no emotion sent"), never refused or invented.
func TestAgentTurnEmotion(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	in := v2Inner(alice, bob, KindAnswer)
	in.Origin = "agent:claude"
	if _, err := Seal(in, alice.id.Sign, r); err == nil {
		t.Fatal("an agent turn without an emotion was sealed")
	}
	in.Emotion = "curious"
	if _, err := Seal(in, alice.id.Sign, r); err != nil {
		t.Fatal(err)
	}
	in.Emotion = ""
	got, err := Open(sealUnchecked(t, in, Version2, alice.id.Sign, bob), bob.id, "bob/b", alice.pub)
	if err != nil || got.Emotion != "" || got.Origin != "agent:claude" {
		t.Fatalf("received agent turn without emotion: %+v %v", got, err)
	}
	human := v2Inner(alice, bob, KindMessage)
	if _, err := Seal(human, alice.id.Sign, r); err != nil {
		t.Fatalf("a person's turn needs no emotion: %v", err)
	}
}
