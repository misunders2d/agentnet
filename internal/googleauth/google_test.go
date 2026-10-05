package googleauth

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testgoogle"
)

func TestGoogleCredentialVerification(t *testing.T) {
	i := testgoogle.New(t)
	v, err := New(i.Client, []string{testgoogle.ClientID})
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	pub := id.Public("google/desk")
	ctx := context.Background()
	token := i.IDToken(t, pub, "person@example.com", nil)
	c, err := v.Verify(ctx, token, protocol.GoogleNonce(pub))
	if err != nil || c.Email != "person@example.com" || c.Name != "Fixture Person" {
		t.Fatalf("valid credential: %+v %v", c, err)
	}
	for _, tc := range []struct {
		name   string
		change map[string]any
	}{
		{"issuer", map[string]any{"iss": "https://attacker.example"}},
		{"audience", map[string]any{"aud": "other.apps.googleusercontent.com"}},
		{"expired", map[string]any{"exp": time.Now().Add(-time.Second).Unix()}},
		{"expiry-boundary", map[string]any{"exp": time.Now().Unix()}},
		{"future", map[string]any{"iat": time.Now().Add(time.Hour).Unix()}},
		{"unverified", map[string]any{"email_verified": false}},
		{"string-verified", map[string]any{"email_verified": "true"}},
		{"nonce", map[string]any{"nonce": "another-key"}},
		{"subject", map[string]any{"sub": ""}},
		{"email", map[string]any{"email": "Person <p@example.com>"}},
		{"authorized-party", map[string]any{"azp": "wrong-client"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := i.IDToken(t, pub, "person@example.com", tc.change)
			_, err := v.Verify(ctx, bad, protocol.GoogleNonce(pub))
			if err != ErrCredential {
				t.Fatalf("accepted invalid claim: %v", err)
			}
			if strings.Contains(err.Error(), bad) {
				t.Fatal("credential leaked into error")
			}
		})
	}
	other := testgoogle.New(t)
	if _, err := v.Verify(ctx, other.IDToken(t, pub, "person@example.com", nil), protocol.GoogleNonce(pub)); err != ErrCredential {
		t.Fatal("forged signature accepted")
	}
	newKey, _ := identity.Generate()
	if _, err := v.Verify(ctx, token, protocol.GoogleNonce(newKey.Public("google/desk"))); err != ErrCredential {
		t.Fatal("stolen token enrolled another key")
	}
	if _, err := v.Verify(ctx, i.IDToken(t, pub, "person@example.com", map[string]any{"iss": "accounts.google.com"}), protocol.GoogleNonce(pub)); err != nil {
		t.Fatal(err)
	}
}
