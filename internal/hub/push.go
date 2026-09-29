package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

// Web Push from the Hub (docs/revival/NOTIFY.md §5-6): RFC 8291 encryption
// and VAPID by github.com/SherClockHolmes/webpush-go (not audited), sent
// through a client of ours that reaches only public addresses.

// pushKeyFile holds the Hub's VAPID key pair, owner-only, in the data
// directory: kept across updates and in backups.
const pushKeyFile = "push.key"

type pushKeys struct {
	Private   string `json:"private"` // base64url (no padding), the 32-byte P-256 scalar
	publicKey string // base64url (no padding), the uncompressed point
}

// loadOrCreatePushKey reads the VAPID key pair, creating it the first time.
func loadOrCreatePushKey(dir string) (pushKeys, error) {
	path := filepath.Join(dir, pushKeyFile)
	var k pushKeys
	data, err := secfile.Read(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &k); err != nil {
			return k, errors.New("push.key is not a push key; remove it to make a new one (devices then subscribe again)")
		}
	case errors.Is(err, os.ErrNotExist):
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return k, err
		}
		d, err := priv.Bytes()
		if err != nil {
			return k, err
		}
		k.Private = base64.RawURLEncoding.EncodeToString(d)
		data, _ := json.Marshal(k)
		if err := secfile.Write(path, data); err != nil {
			return k, err
		}
	default:
		return k, err
	}
	d, err := base64.RawURLEncoding.DecodeString(k.Private)
	if err != nil {
		return k, errors.New("push.key is damaged")
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return k, errors.New("push.key is damaged")
	}
	pub, err := priv.PublicKey.Bytes()
	if err != nil {
		return k, err
	}
	k.publicKey = base64.RawURLEncoding.EncodeToString(pub)
	return k, nil
}

// errNotPublic refuses a connection to an address that is not public.
var errNotPublic = errors.New("push: destination address is not public")

// blockedPrefixes are special-purpose ranges a push never goes to, besides
// what netip classifies as loopback, private, link-local, multicast or
// unspecified.
var blockedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2001:db8::/32", "2002::/16", "fec0::/10",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// publicAddr reports whether a push may connect to ip.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap() // an IPv4-mapped IPv6 address is judged as IPv4
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || !ip.IsGlobalUnicast() {
		return false
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// dialAllowed decides each address a push dials, after name resolution, so
// there is no gap between a check and the connection (tests may widen it).
var dialAllowed = publicAddr

// newPushClient makes the only client that sends pushes: public
// destinations only (checked at dial time), no proxy, no redirects,
// system CAs, bounded time.
func newPushClient() *http.Client {
	dialer := &net.Dialer{Timeout: pushTimeout, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil || !dialAllowed(ap.Addr()) {
			return errNotPublic
		}
		return nil
	}}
	tr := &http.Transport{
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    pushTimeout,
		ResponseHeaderTimeout:  pushTimeout,
		MaxResponseHeaderBytes: 16 << 10,
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           8,
		IdleConnTimeout:        90 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: pushTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// webPusher sends pushes with the Hub's VAPID key through client.
func webPusher(keys pushKeys, subject string, client *http.Client) pushSender {
	return func(ctx context.Context, sub protocol.PushSubscription, payload []byte, topic string) (int, time.Duration, error) {
		resp, err := webpush.SendNotificationWithContext(ctx, payload,
			&webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{Auth: sub.Auth, P256dh: sub.P256DH}},
			&webpush.Options{HTTPClient: client, RecordSize: protocol.PushPayloadRecord, Subscriber: subject, Topic: topic,
				TTL: pushTTL, Urgency: webpush.UrgencyHigh, VAPIDPublicKey: keys.publicKey, VAPIDPrivateKey: keys.Private})
		if err != nil {
			// The error may carry the endpoint (a *url.Error does): never
			// passed on.
			if errors.Is(err, errNotPublic) {
				return 0, 0, errNotPublic
			}
			return 0, 0, errors.New("push: the push service could not be reached")
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		var wait time.Duration
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			wait = time.Duration(s) * time.Second
		} else if t, err := http.ParseTime(resp.Header.Get("Retry-After")); err == nil {
			wait = time.Until(t)
		}
		return resp.StatusCode, wait, nil
	}
}
