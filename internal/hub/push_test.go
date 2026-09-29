package hub

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// Pushes reach only public addresses: every special-purpose range, in
// IPv4, IPv6 and IPv4-mapped forms, is refused.
func TestPushPublicAddressesOnly(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "::1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "0.0.0.0", "0.1.2.3",
		"255.255.255.255", "224.0.0.1", "198.18.0.1", "192.0.2.1", "240.0.0.1", "::", "fc00::1", "fd12::1", "fe80::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254", "64:ff9b::a00:1", "2002:7f00:1::1", "2001:db8::1", "2001::1", "100::1",
	} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s allowed", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "142.250.64.100", "17.253.144.10", "2606:4700:4700::1111", "2a00:1450:4001:80b::200a"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s refused", s)
		}
	}
}

// The check is made on the address being dialed, after resolution: a name
// that resolves to loopback (as a rebinding would) is refused, and the
// error never carries the endpoint.
func TestPushDialRefusesLoopbackName(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("reached a loopback server") }))
	defer srv.Close()
	port := srv.URL[strings.LastIndex(srv.URL, ":"):]
	send := webPusher(testPushKeys(t), "https://hub.example.test", newPushClient())
	_, _, err := send(context.Background(), protocol.PushSubscription{Endpoint: "https://localhost" + port + "/SECRET", P256DH: testReceiver(t).p256dh, Auth: testReceiver(t).auth},
		[]byte(`{"v":1}`), "summary")
	if !errors.Is(err, errNotPublic) || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "localhost") {
		t.Fatalf("dial to a loopback name: %v", err)
	}
}

type receiver struct {
	priv         *ecdh.PrivateKey
	secret       []byte
	p256dh, auth string
}

var (
	rcvOnce sync.Once
	rcv     receiver
)

func testReceiver(t *testing.T) receiver {
	rcvOnce.Do(func() {
		priv, err := ecdh.P256().GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		secret := make([]byte, 16)
		rand.Read(secret)
		rcv = receiver{priv: priv, secret: secret, p256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
			auth: base64.RawURLEncoding.EncodeToString(secret)}
	})
	return rcv
}

func testPushKeys(t *testing.T) pushKeys {
	t.Helper()
	k, err := loadOrCreatePushKey(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// decryptPush undoes RFC 8291 (aes128gcm, one record) with the receiver's
// keys, independently of the library that encrypted it.
func decryptPush(t *testing.T, r receiver, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatal("short push body")
	}
	salt, idlen := body[:16], int(body[20])
	asPublic, ct := body[21:21+idlen], body[21+idlen:]
	pub, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := r.priv.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	prkKey, _ := hkdf.Extract(sha256.New, shared, r.secret)
	info := append(append([]byte("WebPush: info\x00"), r.priv.PublicKey().Bytes()...), asPublic...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("push does not decrypt: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("push record not terminated as the last record")
	}
	return plain[:len(plain)-1]
}

// verifyVAPID checks the Authorization header: an ES256 JWT for the push
// service's origin, signed by the Hub's VAPID key, which it names.
func verifyVAPID(t *testing.T, header string, keys pushKeys, aud, sub string) {
	t.Helper()
	var jwt, k string
	for _, part := range strings.Split(strings.TrimPrefix(header, "vapid "), ", ") {
		switch {
		case strings.HasPrefix(part, "t="):
			jwt = part[2:]
		case strings.HasPrefix(part, "k="):
			k = part[2:]
		}
	}
	if k != keys.publicKey {
		t.Fatalf("VAPID k %q, want the Hub's key", k)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatal("VAPID token is not a JWT")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(k)
	x, y := elliptic.Unmarshal(elliptic.P256(), raw) //nolint:staticcheck // a test's independent parse
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID token signature does not verify")
	}
	var hdr struct{ Alg string }
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	json.Unmarshal(h, &hdr)
	var claims struct {
		Aud, Sub string
		Exp      int64
	}
	c, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(c, &claims)
	if hdr.Alg != "ES256" || claims.Aud != aud || claims.Sub != sub || claims.Exp < time.Now().Unix() || claims.Exp > time.Now().Add(24*time.Hour).Unix() {
		t.Fatalf("VAPID token: alg %s, claims %+v", hdr.Alg, claims)
	}
}

// A push is what RFC 8291 and VAPID say: the receiver decrypts exactly the
// payload from one fixed-size record, the token verifies, and the headers
// carry the topic, TTL and urgency; a redirect is not followed.
func TestPushWireFormat(t *testing.T) {
	var mu sync.Mutex
	var got *http.Request
	var body []byte
	redirected := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/push/redirect":
			http.Redirect(w, r, "/push/elsewhere", http.StatusFound)
		case "/push/elsewhere":
			redirected = true
		default:
			got, body = r, nil
			body, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()
	old := dialAllowed
	dialAllowed = func(ip netip.Addr) bool { return ip.IsLoopback() } // the test service only
	t.Cleanup(func() { dialAllowed = old })
	client := newPushClient()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig.RootCAs = pool

	keys, r := testPushKeys(t), testReceiver(t)
	send := webPusher(keys, "https://hub.example.test", client)
	sub := protocol.PushSubscription{Endpoint: srv.URL + "/push/abc", P256DH: r.p256dh, Auth: r.auth}
	payload, _ := json.Marshal(protocol.PushPayload{V: 1, Channel: "CRTkM8HtV3rVNaVsnOnalw"})
	status, _, err := send(context.Background(), sub, payload, "CRTkM8HtV3rVNaVsnOnalw")
	if err != nil || status != http.StatusCreated {
		t.Fatalf("push: %d %v", status, err)
	}
	mu.Lock()
	if len(body) != protocol.PushPayloadRecord {
		t.Fatalf("push body %d bytes, want one %d-byte record", len(body), protocol.PushPayloadRecord)
	}
	if plain := decryptPush(t, r, body); !bytes.Equal(plain, payload) {
		t.Fatalf("decrypted %q", plain)
	}
	if got.Header.Get("Content-Encoding") != "aes128gcm" || got.Header.Get("TTL") != "86400" || got.Header.Get("Topic") != "CRTkM8HtV3rVNaVsnOnalw" ||
		got.Header.Get("Urgency") != "high" {
		t.Fatalf("headers: %v", got.Header)
	}
	verifyVAPID(t, got.Header.Get("Authorization"), keys, srv.URL, "https://hub.example.test")
	mu.Unlock()

	sub.Endpoint = srv.URL + "/push/redirect"
	if status, _, err := send(context.Background(), sub, payload, "summary"); err != nil || status != http.StatusFound {
		t.Fatalf("redirect: %d %v", status, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if redirected {
		t.Fatal("a redirect was followed")
	}
}

// The VAPID key is kept owner-only and reused.
func TestPushKeyKept(t *testing.T) {
	dir := t.TempDir()
	a, err := loadOrCreatePushKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := loadOrCreatePushKey(dir)
	if err != nil || a.publicKey != b.publicKey || a.Private != b.Private || len(a.publicKey) != 87 {
		t.Fatalf("key not kept: %v", err)
	}
}

// A backup carries the VAPID key and a restore brings back the same one, so
// devices' subscriptions stay valid.
func TestPushKeyInBackup(t *testing.T) {
	h, _, _ := testHub(t)
	want := h.push.publicKey
	dir := h.cfg.DataDir
	h.Close()
	m, err := OpenMaintenance(dir)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := m.Backup(&buf); err != nil {
		t.Fatal(err)
	}
	m.Close()
	restored := filepath.Join(t.TempDir(), "r")
	if _, err := Restore(&buf, restored); err != nil {
		t.Fatal(err)
	}
	k, err := loadOrCreatePushKey(restored)
	if err != nil || k.publicKey != want {
		t.Fatalf("restored push key differs: %v", err)
	}
}
