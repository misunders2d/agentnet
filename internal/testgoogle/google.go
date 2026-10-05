// Package testgoogle is an entirely local OIDC/JWKS fixture. Its transport
// refuses every request except Google's fixed JWKS and OAuth token paths;
// no test can accidentally call Google.
package testgoogle

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const ClientID = "fixture.apps.googleusercontent.com"

type Issuer struct {
	key        *rsa.PrivateKey
	Client     *http.Client
	Token      string
	OnExchange func(*http.Request)
}
type transport func(*http.Request) (*http.Response, error)

func (t transport) RoundTrip(r *http.Request) (*http.Response, error) { return t(r) }

func New(t testing.TB) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	i := &Issuer{key: key}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/oauth2/v3/certs" {
			w.Header().Set("Cache-Control", "public, max-age=60")
			json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "local-fixture", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
			return
		}
		if r.URL.Path == "/token" {
			if i.OnExchange != nil {
				i.OnExchange(r)
			}
			json.NewEncoder(w).Encode(map[string]any{"access_token": "local-unused-access", "token_type": "Bearer", "expires_in": 3600, "id_token": i.Token})
			return
		}
		http.Error(w, "fixture path refused", 404)
	}))
	t.Cleanup(s.Close)
	local, _ := url.Parse(s.URL)
	i.Client = &http.Client{Transport: transport(func(r *http.Request) (*http.Response, error) {
		if !(r.URL.Scheme == "https" && (r.URL.Host == "www.googleapis.com" && r.URL.Path == "/oauth2/v3/certs" || r.URL.Host == "oauth2.googleapis.com" && r.URL.Path == "/token")) {
			return nil, errors.New("fixture refuses external request")
		}
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Scheme = local.Scheme
		u.Host = local.Host
		copy.URL = &u
		copy.Host = local.Host
		return s.Client().Transport.RoundTrip(copy)
	}), Timeout: 5 * time.Second}
	return i
}

func (i *Issuer) IDToken(t testing.TB, p identity.Public, email string, changes map[string]any) string {
	t.Helper()
	_, domain, _ := strings.Cut(email, "@")
	c := map[string]any{"iss": "https://accounts.google.com", "aud": ClientID, "sub": "fixture-subject-" + email, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "email": email, "email_verified": true, "name": "Fixture Person", "hd": domain, "nonce": protocol.GoogleNonce(p)}
	for k, v := range changes {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "local-fixture"})
	b, _ := json.Marshal(c)
	body := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return body + "." + base64.RawURLEncoding.EncodeToString(sig)
}
