package envelope

import (
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
)

type party struct {
	id  *identity.Identity
	pub identity.Public
}

func newParty(t *testing.T, addr string) party {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return party{id, id.Public(addr)}
}

func seal(t *testing.T, from, to party, body string) Envelope {
	t.Helper()
	r, err := to.pub.Recipient()
	if err != nil {
		t.Fatal(err)
	}
	env, err := Seal(Inner{ID: "m1", From: from.pub.Address, To: to.pub.Address, TS: time.Now().Unix(), Kind: KindMessage, Body: body}, from.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestRoundTrip(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	env := seal(t, alice, bob, "hello")
	in, err := Open(env, bob.id, bob.pub.Address, alice.pub)
	if err != nil || in.Body != "hello" {
		t.Fatalf("Open = %+v, %v", in, err)
	}
}

func TestTamperRejected(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	env := seal(t, alice, bob, "hello")
	env.CT[len(env.CT)-1] ^= 1
	if _, err := Open(env, bob.id, bob.pub.Address, alice.pub); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestWrongRecipientRejected(t *testing.T) {
	alice, bob, carol := newParty(t, "alice/a"), newParty(t, "bob/b"), newParty(t, "carol/c")
	env := seal(t, alice, carol, "for carol")
	if _, err := Open(env, bob.id, bob.pub.Address, alice.pub); !errors.Is(err, ErrNotForMe) {
		t.Fatalf("misrouted envelope: %v", err)
	}
	// Even if the Hub rewrites To, bob cannot decrypt carol's ciphertext.
	env.To = bob.pub.Address
	if _, err := Open(env, bob.id, bob.pub.Address, alice.pub); err == nil {
		t.Fatal("rewritten recipient accepted")
	}
}

func TestResignedCiphertextRejected(t *testing.T) {
	alice, bob, mallory := newParty(t, "alice/a"), newParty(t, "bob/b"), newParty(t, "mallory/m")
	env := seal(t, alice, bob, "from alice")
	// Mallory claims alice's ciphertext as her own and signs it validly.
	env.From = mallory.pub.Address
	env.Sig = ed25519.Sign(mallory.id.Sign, env.signed())
	if _, err := Open(env, bob.id, bob.pub.Address, mallory.pub); err == nil {
		t.Fatal("re-signed ciphertext accepted")
	}
}

func TestManifestMustMatchSignedBlobs(t *testing.T) {
	alice, bob := newParty(t, "alice/a"), newParty(t, "bob/b")
	r, _ := bob.pub.Recipient()
	blob := Blob{ID: "0123456789abcdef0123456789abcdef", Size: 100, SHA256: strings.Repeat("a", 64)}
	in := Inner{ID: "m1", From: alice.pub.Address, To: bob.pub.Address, TS: 1, Kind: KindMessage,
		Attachments: []Attachment{{Blob: blob, Name: "f.txt", Size: 10, SHA256: strings.Repeat("b", 64)}}}
	env, err := Seal(in, alice.id.Sign, r)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(env, bob.id, bob.pub.Address, alice.pub); err != nil || got.Attachments[0].Name != "f.txt" {
		t.Fatalf("Open = %+v, %v", got, err)
	}
	// A signed outer reference that differs from the encrypted manifest.
	env.Blobs[0].SHA256 = strings.Repeat("c", 64)
	env.Sig = ed25519.Sign(alice.id.Sign, env.signed())
	if _, err := Open(env, bob.id, bob.pub.Address, alice.pub); err == nil {
		t.Fatal("mismatched blob reference accepted")
	}
}

// Text that shows nothing is blank; any visible character is not.
func TestBlank(t *testing.T) {
	for _, blank := range []string{"", " \t\n", "\u200b", "\u3000\u200b", " \u200c\u200d\u2060\ufeff ", "\u200e\u200f\u202e\u2066\u2069", "\u3164\u115f\ufe0f\u00ad", "\u0085\u00a0\U000e0041", "\u2800", " \u2800\u200b"} {
		if !Blank(blank) {
			t.Errorf("%q is not blank", blank)
		}
	}
	for _, shown := range []string{"a", ".", "👍", "a\u200bb", "\u2764\ufe0f", "\u06dd", "\u2800x"} {
		if Blank(shown) {
			t.Errorf("%q is blank", shown)
		}
	}
}
