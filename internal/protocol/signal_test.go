package protocol

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/identity"
	"strings"
	"testing"
	"time"
)

const SignalVectorCanonical = `agentnet-live-signal-v1
{"v":1,"id":"00112233445566778899aabbccddeeff","from":"vitalii/desk","to":"bob/phone","ts":1790000000123,"session":"ffeeddccbbaa99887766554433221100","ct":"AQID"}`

// CT AQID is canonical/signature test material, not an age file. Actual
// age sealing/opening, repeated header binding and privacy are tested below.
const SignalVectorHash = "1559387675f7d85dc16f15940635983f54c847d45594a94dfad5ed19ecc95f4e"
const SignalVectorSignature = "223fa5e693c6e656b3e0af7fc18ce090222a8308eac31e83289323751a0faad371667938df076f8031d1aacf014515f4ae1b2e0add58843c20d5f03eccb7a605"

func TestSignalCanonicalVectorAndReplayWindow(t *testing.T) {
	id, _ := vecDesk()
	s := Signal{V: 1, ID: vecSession, From: "vitalii/desk", To: "bob/phone", TS: 1790000000123, Session: "ffeeddccbbaa99887766554433221100", CT: []byte{1, 2, 3}}
	s.Sig = ed25519.Sign(id.Sign, s.Canonical())
	if string(s.Canonical()) != SignalVectorCanonical || hashHex(s.Canonical()) != SignalVectorHash || hex.EncodeToString(s.Sig) != SignalVectorSignature {
		t.Fatalf("vector: %s\n%s\n%x", s.Canonical(), hashHex(s.Canonical()), s.Sig)
	}
	now := time.UnixMilli(s.TS)
	if err := s.Verify(id.Sign.Public().(ed25519.PublicKey), now); err != nil {
		t.Fatal(err)
	}
	w := ReplayWindow{}
	if !w.Accept("first", now.Add(SignalTTL), now, 1) || w.Accept("first", now.Add(SignalTTL), now, 1) || w.Accept("other", now.Add(SignalTTL), now, 1) {
		t.Fatal("replay/saturation evicted unexpired evidence")
	}
	if !w.Accept("other", now.Add(2*SignalTTL), now.Add(SignalTTL), 1) {
		t.Fatal("expired memory did not clear")
	}
}
func TestSignalAgeScopeTimeAndDestinationBinding(t *testing.T) {
	from, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	to, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	realm := NewID()
	in := TypingPlain{V: 1, ID: NewID(), From: "alice/desk", To: "bob/desk", TS: now.UnixMilli(), Session: NewID(), Realm: realm, Conv: strings.Repeat("a", 64), Origin: "human", Active: true}
	s, err := SealTyping(in, from.Sign, to.Public(in.To))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(s)
	if strings.Contains(string(raw), in.Conv) || strings.Contains(string(raw), realm) || strings.Contains(string(raw), "active") || strings.Contains(string(raw), "human") {
		t.Fatal("relay saw scope/composer plaintext")
	}
	got, err := OpenTyping(s, to, in.To, from.Public(in.From), realm, now)
	if err != nil || got != in {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
	for _, tc := range []struct {
		name, realm, address string
		now                  time.Time
	}{
		{"foreign realm", NewID(), in.To, now}, {"foreign destination", realm, "carol/desk", now}, {"expired", realm, in.To, now.Add(SignalTTL)}, {"future", realm, in.To, now.Add(-2 * time.Second)},
	} {
		if _, err := OpenTyping(s, to, tc.address, from.Public(in.From), tc.realm, tc.now); err == nil {
			t.Fatalf("accepted %s", tc.name)
		}
	}
	bad := s
	bad.Session = NewID()
	bad.Sig = ed25519.Sign(from.Sign, bad.Canonical())
	if _, err := OpenTyping(bad, to, in.To, from.Public(in.From), realm, now); err == nil {
		t.Fatal("re-signed outer replaced encrypted session")
	}
	bad = s
	bad.Sig = ed25519.Sign(to.Sign, bad.Canonical())
	if _, err := OpenTyping(bad, to, in.To, from.Public(in.From), realm, now); err == nil {
		t.Fatal("wrong key accepted")
	}
	if _, err := ParseSignal([]byte(strings.Replace(string(raw), `"v":1`, `"v":1,"body":"typing"`, 1))); err == nil {
		t.Fatal("unrecognized plaintext field accepted")
	}
}
