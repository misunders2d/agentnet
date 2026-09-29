package envelope

import (
	"encoding/hex"

	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/protocol"
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

// The attention hint and channel are version 2 outer fields covered by the
// signature: both or neither, a channel's shape, never on version 1; an
// envelope without them signs exactly as before.
func TestAttentionSigned(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	ch := protocol.NotifyChannel(testConv, bob.pub.Fingerprint())
	env, err := SealAttention(v2Inner(alice, bob, KindMessage), alice.id.Sign, r, ch)
	if err != nil || !env.Attn || env.Chan != ch {
		t.Fatalf("SealAttention = %+v, %v", env, err)
	}
	if err := env.VerifySig(alice.pub.SignKey); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(env, bob.id, bob.pub.Address, alice.pub); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*Envelope){
		"hint removed":    func(e *Envelope) { e.Attn, e.Chan = false, "" },
		"channel changed": func(e *Envelope) { e.Chan = protocol.NotifyChannel(testConv, alice.pub.Fingerprint()) },
	} {
		e := env
		mut(&e)
		if e.VerifySig(alice.pub.SignKey) == nil {
			t.Errorf("%s: still verifies", name)
		}
	}
	for name, e := range map[string]Envelope{
		"attn without channel": {Attn: true},
		"channel without attn": {Chan: ch},
		"bad channel":          {Attn: true, Chan: "x"},
	} {
		base := env
		base.Attn, base.Chan = e.Attn, e.Chan
		base.Sig = ed25519.Sign(alice.id.Sign, base.signed())
		if base.VerifySig(alice.pub.SignKey) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	v1 := seal(t, alice, bob, "old")
	v1.Attn, v1.Chan = true, ch
	v1.Sig = ed25519.Sign(alice.id.Sign, v1.signed())
	if v1.VerifySig(alice.pub.SignKey) == nil {
		t.Fatal("attention on a version 1 envelope accepted")
	}
	if _, err := SealAttention(Inner{V: Version, ID: "m", From: "alice/a", To: "bob/b", Kind: KindMessage}, alice.id.Sign, r, ch); err == nil {
		t.Fatal("SealAttention made a version 1 envelope")
	}
	plain, _ := Seal(v2Inner(alice, bob, KindMessage), alice.id.Sign, r)
	if strings.Contains(string(plain.signed()), "attn") || strings.Contains(string(plain.signed()), "chan") {
		t.Fatal("an envelope without attention signs new fields")
	}
}

// Canonical bytes and signature of an envelope with attention, for other
// implementations (the browser device): key from an all-zero seed.
func TestAttentionVector(t *testing.T) {
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	fp := "01234567-89abcdef-01234567-89abcdef"
	env := Envelope{V: Version2, ID: testLID, From: "alice/a", To: "bob/b", TS: 1790000000, Kind: KindMessage, CT: []byte("ciphertext"),
		Attn: true, Chan: protocol.NotifyChannel(testConv, fp)}
	env.Sig = ed25519.Sign(key, env.signed())
	want := "agentnet-envelope-v2\n" + `{"v":2,"id":"00112233445566778899aabbccddeeff","from":"alice/a","to":"bob/b","ts":1790000000,` +
		`"kind":"message","ct":"Y2lwaGVydGV4dA==","attn":true,"chan":"CRTkM8HtV3rVNaVsnOnalw"}`
	if got := string(env.signed()); got != want {
		t.Fatalf("canonical bytes:\n got %s\nwant %s", got, want)
	}
	if hex.EncodeToString(env.Sig) != "71e8952944d3efcadea155cf5a53e05fd50b7f19c31895fa2f3918f1fc6ada109b77c1ba0e3aa23ce34c70cba00359db052e4eab7324b6372315d75e009d3705" {
		t.Fatalf("signature %x", env.Sig)
	}
}
