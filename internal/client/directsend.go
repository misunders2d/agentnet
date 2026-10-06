package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

var (
	// dialTimeout bounds connecting and the TLS handshake to a peer; each
	// payload request then has the ordinary request timeout.
	dialTimeout = 3 * time.Second
	// routeCooldown skips a peer endpoint that just failed, for this sender only.
	routeCooldown = 5 * time.Minute
)

// ErrSessionExpired means the addressed session is no longer live.
var ErrSessionExpired = errors.New("the addressed session has ended (send with fallback to reach the agent's inbox)")

// sessions asks the Hub which sessions of address are live.
func (a *Agent) sessions(ctx context.Context, address string) ([]protocol.SessionInfo, error) {
	label, name, err := protocol.SplitAddress(address)
	if err != nil {
		return nil, err
	}
	var out []protocol.SessionInfo
	err = a.hub.do(ctx, "GET", "/v1/agents/"+label+"/"+name+"/sessions", nil, &out)
	return out, err
}

// Sessions lists the live sessions of address.
func (a *Agent) Sessions(ctx context.Context, address string) ([]protocol.SessionInfo, error) {
	return a.sessions(ctx, address)
}

// directRoute picks a connected session's endpoint for the target whose ad
// is signed by the recipient's trusted key and has not failed recently.
func (a *Agent) directRoute(infos []protocol.SessionInfo, to, session string, peer identity.Public) *protocol.SessionAd {
	for _, info := range infos {
		ad := info.Ad
		if !info.Connected || ad.Endpoint == "" || ad.Address != to || (session != "" && ad.Session != session) {
			continue
		}
		if err := ad.Verify(peer.SignKey); err != nil {
			a.Logf("ignoring session ad for %s: %v", to, err)
			continue
		}
		if cooling, err := a.store.routeCooling(ad.Endpoint, time.Now()); err != nil || cooling {
			continue
		}
		return &ad
	}
	return nil
}

func live(infos []protocol.SessionInfo, session string) bool {
	for _, info := range infos {
		if info.Ad.Session == session {
			return true
		}
	}
	return false
}

// directConn connects to a peer endpoint trusting exactly the certificate
// in its signed ad, with no proxy and no redirects.
func (a *Agent) directConn(ad protocol.SessionAd) (*hubConn, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(ad.CertPEM)) {
		return nil, errors.New("invalid certificate in session ad")
	}
	var rt http.RoundTripper = &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: requestTimeout,
		TLSClientConfig:       &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13},
	}
	if wrapTransport != nil {
		rt = wrapTransport(rt)
	}
	return &hubConn{
		base:    ad.Endpoint,
		agent:   a.Address,
		key:     a.id.Sign,
		timeout: requestTimeout,
		http: &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("redirects are not followed")
		}},
	}, nil
}

// directDeliveryStopped is an authority/capability refusal, not a failed route.
// Its caller must keep the stored state rather than retrying through the Hub.
type directDeliveryStopped struct{ cause error }

func (e *directDeliveryStopped) Error() string {
	return "direct handoff stopped by current delivery gate"
}
func (e *directDeliveryStopped) Unwrap() error { return e.cause }

// sendDirect uploads attachments and the envelope straight to the peer. It
// succeeds only when the peer answers that it stored the message.
func (a *Agent) sendDirect(ctx context.Context, env envelope.Envelope, ad protocol.SessionAd) (protocol.Receipt, error) {
	var r protocol.Receipt
	conn, err := a.directConn(ad)
	if err != nil {
		return r, err
	}
	for _, b := range env.Blobs {
		if err := a.upload(ctx, conn, "/v1/direct/blobs", env.To, b); err != nil {
			return r, err
		}
	}
	// Uploads and connection setup take time. Recheck exact stored authority
	// immediately before handoff, just as the relay path does.
	if ok, err := a.receiverOriginalMayDeliver(env); err != nil || !ok {
		return r, &directDeliveryStopped{cause: err}
	}
	if ok, err := a.mayDeliver(env); err != nil || !ok {
		return r, &directDeliveryStopped{cause: err}
	}
	if err := conn.do(ctx, "POST", "/v1/direct/messages", env, &r); err != nil {
		return r, err
	}
	if r.ID != env.ID || (r.State != protocol.StateDelivered && r.State != protocol.StateQuarantined) {
		return r, errors.New("peer did not confirm storing the message")
	}
	return r, nil
}
