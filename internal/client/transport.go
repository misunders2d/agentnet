package client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/sqlitedb"
)

// ErrRevoked means this agent has been revoked by a Hub admin.
var ErrRevoked = errors.New("this agent has been revoked")

// HubError is a non-2xx answer from the Hub.
type HubError struct {
	Status int
	Code   string
	Msg    string
	// Latest and URL are an update_required refusal's (HTTP 426): the
	// release this device must run before the Hub serves it again, and the
	// release page the Hub names (shown to people, never downloaded).
	Latest, URL string
}

func (e *HubError) Error() string { return fmt.Sprintf("hub: %s (%d)", e.Msg, e.Status) }

// Is maps the Hub's answers that end this agent for good to their errors:
// revoked (ErrRevoked), and a device link refused on the other device
// (ErrLinkRefused) or approved by nobody in time (ErrLinkExpired).
func (e *HubError) Is(target error) bool {
	switch target {
	case ErrRevoked:
		return e.Code == protocol.CodeRevoked
	case ErrLinkRefused:
		return e.Code == protocol.CodeLinkRefused
	case ErrLinkExpired:
		return e.Code == protocol.CodeLinkExpired
	case ErrUpdateRequired:
		return e.Code == codeUpdateRequired
	}
	return false
}

// errPermanent marks local failures that retrying cannot fix.
var errPermanent = errors.New("cannot be retried")

// retryable reports whether a failed call may succeed later unchanged. Send
// paths use it: a local failure keeps the copy queued for the next try.
// Receiving decides with transient instead.
func retryable(err error) bool {
	if errors.Is(err, errPermanent) {
		return false
	}
	var he *HubError
	if errors.As(err, &he) {
		// A Hub that requires a newer AgentNet takes the same request from
		// the updated program: it waits, never fails (updaterequired.go).
		return he.Status >= 500 || he.Status == http.StatusTooManyRequests || he.Code == codeUpdateRequired
	}
	return err != nil
}

// transient reports whether err is a typed failure that receiving the same
// message again may get past: the Hub or the network out of reach or busy
// (no answer, 5xx, 429), the Hub refusing this build until it is updated
// (refusedUntilUpdate), a cancelled or timed-out request, or a database
// another process held. Anything else (a 4xx answer, a failed check, a
// record this program cannot read) fails the same way every time, so it
// must never leave a message unacknowledged at the head of the push stream.
func transient(err error) bool {
	if err == nil || errors.Is(err, errPermanent) {
		return false
	}
	var he *HubError
	if errors.As(err, &he) {
		return he.Status >= 500 || he.Status == http.StatusTooManyRequests || refusedUntilUpdate(err)
	}
	var ne net.Error
	var ue *url.Error
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.As(err, &ne) || errors.As(err, &ue) || sqlitedb.Busy(err)
}

// refusedUntilUpdate reports whether the Hub refused the request because
// this build must be updated first (HTTP 426 update_required). It is no
// failure of the message being received: the Hub keeps it in custody and
// delivers it again after the update, so it is never held or acknowledged.
func refusedUntilUpdate(err error) bool {
	var he *HubError
	return errors.As(err, &he) && he.Status == http.StatusUpgradeRequired
}

// requestTimeout bounds every ordinary Hub request and the wait for the push
// stream's response headers; the stream body is bounded by its ping watchdog.
// A variable so tests can shorten it.
var requestTimeout = 30 * time.Second

// Only our transport bound diagnoses a stalled route. A caller's shorter
// deadline ends that request, not the other requests sharing its HTTP/2 socket.
// The private cause must keep the standard deadline and net.Error contracts:
// net/http returns it to callers when this bound expires.
var errHubRequestTimeout = &hubRequestTimeoutError{context.DeadlineExceeded}

type hubRequestTimeoutError struct{ error }

func (e *hubRequestTimeoutError) Unwrap() error { return e.error }
func (*hubRequestTimeoutError) Timeout() bool   { return true }
func (*hubRequestTimeoutError) Temporary() bool { return true }

// wrapTransport lets tests inject network faults; nil in production.
var wrapTransport func(http.RoundTripper) http.RoundTripper

// hubConn talks to one Hub, pinning its certificate when the invite carried one.
type hubConn struct {
	base        string
	agent       string
	key         ed25519.PrivateKey
	http        *http.Client
	timeout     time.Duration
	connections *hubConnections
	// workspaceCheck, when set, gates every request except the version
	// probe on the pinned workspace identity (workspaces_realmguard.go).
	workspaceCheck func(context.Context, string) error
	// gate, when set, learns of the Hub's update_required refusals and,
	// in the daemon, refuses here what the Hub would (updaterequired.go).
	gate *updateGate
}

func newHubConn(base, certPEM, agent string, key ed25519.PrivateKey) (*hubConn, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS13}
	if certPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(certPEM)) {
			return nil, errors.New("invalid Hub certificate in invite")
		}
		tlsCfg.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsCfg
	// Bounds waiting for response headers on every request, including the
	// push stream, whose long-lived body is bounded by its ping watchdog.
	tr.ResponseHeaderTimeout = requestTimeout
	c := &hubConn{base: base, agent: agent, key: key, timeout: requestTimeout, connections: &hubConnections{}}
	tr.DialContext = c.trackDial(tr.DialContext)
	var rt http.RoundTripper = tr
	if wrapTransport != nil {
		rt = wrapTransport(rt)
	}
	c.http = &http.Client{Transport: rt}
	return c, nil
}

// release closes the idle connections to the Hub and stops dials that no
// request waits for any more: net/http keeps dialing after the request that
// started a dial has ended, so the connection would otherwise reach the Hub
// after its owner was closed.
func (c *hubConn) release() {
	if c != nil {
		c.resetConnections()
		c.http.CloseIdleConnections()
	}
}

// request builds a request with a raw body, signed unless the connection
// has no agent yet. Every request says which program version makes it
// (unsigned): a Hub asks an outdated one to update.
func (c *hubConn) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	if err := c.gate.before(method, path, body); err != nil {
		return nil, err
	}
	if c.workspaceCheck != nil {
		if err := c.workspaceCheck(ctx, path); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set(protocol.VersionHeader, protocol.Version)
	if c.agent != "" {
		protocol.SignRequest(req, c.agent, c.key, body)
	}
	return req, nil
}

// do sends in as JSON (or no body when nil) and decodes a JSON answer into out.
func (c *hubConn) do(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return err
		}
	}
	return c.doBytes(ctx, method, path, body, out)
}

// doBytes sends a raw body within the request timeout and decodes a JSON
// answer into out.
func (c *hubConn) doBytes(ctx context.Context, method, path string, body []byte, out any) error {
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout, errHubRequestTimeout)
	defer cancel()
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	generation := c.connectionGeneration()
	resp, err := c.sendRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := c.check(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, protocol.MaxBody)).Decode(out)
	c.resetAfterTimeout(ctx, err, generation)
	return err
}

// getRange copies bytes [from, to) of path into w within the request timeout.
func (c *hubConn) getRange(ctx context.Context, path string, from, to int64, w io.Writer) error {
	ctx, cancel := context.WithTimeoutCause(ctx, c.timeout, errHubRequestTimeout)
	defer cancel()
	req, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to-1))
	generation := c.connectionGeneration()
	resp, err := c.sendRequest(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := c.check(resp); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("hub ignored range request (%s)", resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, to-from))
	c.resetAfterTimeout(ctx, err, generation)
	if err == nil && n != to-from {
		err = io.ErrUnexpectedEOF
	}
	return err
}

// check is checkStatus, telling the gate of the Hub's update_required refusals.
func (c *hubConn) check(resp *http.Response) error {
	err := checkStatus(resp)
	c.gate.after(err)
	return err
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode/100 == 2 {
		return nil
	}
	var e struct { // protocol.Error, and what an update_required refusal adds
		Code   string `json:"code"`
		Error  string `json:"error"`
		Latest string `json:"latest"`
		URL    string `json:"url"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	if resp.StatusCode == http.StatusUpgradeRequired && (e.Error == codeUpdateRequired || e.Code == codeUpdateRequired) {
		return updateRequiredError(e.Latest, e.URL)
	}
	if e.Error == "" {
		e.Error = resp.Status
	}
	return &HubError{Status: resp.StatusCode, Code: e.Code, Msg: e.Error}
}

func decodeStrict(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// A route can disappear while HTTP/2 still has an active push stream. Reset
// sockets, rather than just idle connections, without replacing Transport or
// replaying any request. Generations fence dials and failures from old routes.
type hubConnections struct {
	sync.Mutex
	generation uint64
	sockets    map[*hubSocket]struct{}
	dials      map[*hubDial]struct{}
}
type hubDial struct{ cancel context.CancelFunc }
type hubSocket struct {
	net.Conn
	owner *hubConnections
}

func (s *hubSocket) Close() error {
	s.owner.Lock()
	delete(s.owner.sockets, s)
	s.owner.Unlock()
	return s.Conn.Close()
}
func (c *hubConn) trackDial(dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	// Installed before the transport is used; never initialize concurrently.
	if c.connections == nil {
		c.connections = &hubConnections{}
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		p := c.connections
		ctx, cancel := context.WithCancel(ctx)
		attempt := &hubDial{cancel: cancel}
		p.Lock()
		generation := p.generation
		if p.dials == nil {
			p.dials = map[*hubDial]struct{}{}
		}
		p.dials[attempt] = struct{}{}
		p.Unlock()
		conn, err := dial(ctx, network, address)
		cancel()
		p.Lock()
		delete(p.dials, attempt)
		if err != nil {
			p.Unlock()
			return nil, err
		}
		if generation != p.generation {
			p.Unlock()
			conn.Close()
			return nil, net.ErrClosed
		}
		socket := &hubSocket{Conn: conn, owner: p}
		if p.sockets == nil {
			p.sockets = map[*hubSocket]struct{}{}
		}
		p.sockets[socket] = struct{}{}
		p.Unlock()
		return socket, nil
	}
}
func (c *hubConn) resetConnections() { c.resetConnectionsAt(nil) }
func (c *hubConn) resetConnectionsAt(expected *uint64) {
	p := c.connections
	if p == nil {
		return
	}
	p.Lock()
	if expected != nil && *expected != p.generation {
		p.Unlock()
		return
	}
	p.generation++
	sockets := p.sockets
	p.sockets = nil
	dials := p.dials
	p.dials = nil
	p.Unlock()
	for dial := range dials {
		dial.cancel()
	}
	for socket := range sockets {
		socket.Close()
	}
}
func (c *hubConn) connectionGeneration() uint64 {
	p := c.connections
	if p == nil {
		return 0
	}
	p.Lock()
	defer p.Unlock()
	return p.generation
}
func (c *hubConn) resetAfterTimeout(ctx context.Context, err error, generation uint64) {
	if err == nil || ctx.Err() != nil && context.Cause(ctx) != errHubRequestTimeout {
		return // caller cancellation/deadline says nothing about the shared route
	}
	var networkError net.Error
	if !errors.Is(err, context.Canceled) && errors.As(err, &networkError) && networkError.Timeout() {
		c.resetConnectionsAt(&generation)
	}
}
func (c *hubConn) sendRequest(req *http.Request) (*http.Response, error) {
	generation := c.connectionGeneration()
	resp, err := c.http.Do(req)
	c.resetAfterTimeout(req.Context(), err, generation)
	return resp, err
}
