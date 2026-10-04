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
	"net/http"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

// ErrRevoked means this agent has been revoked by a Hub admin.
var ErrRevoked = errors.New("this agent has been revoked")

// HubError is a non-2xx answer from the Hub.
type HubError struct {
	Status int
	Code   string
	Msg    string
}

func (e *HubError) Error() string { return fmt.Sprintf("hub: %s (%d)", e.Msg, e.Status) }

// Is maps the Hub's revocation answer to ErrRevoked.
func (e *HubError) Is(target error) bool {
	return target == ErrRevoked && e.Code == protocol.CodeRevoked
}

// errPermanent marks local failures that retrying cannot fix.
var errPermanent = errors.New("cannot be retried")

// retryable reports whether a failed call may succeed later unchanged.
func retryable(err error) bool {
	if errors.Is(err, errPermanent) {
		return false
	}
	var he *HubError
	if errors.As(err, &he) {
		return he.Status >= 500
	}
	return err != nil
}

// requestTimeout bounds every ordinary Hub request and the wait for the push
// stream's response headers; the stream body is bounded by its ping watchdog.
// A variable so tests can shorten it.
var requestTimeout = 30 * time.Second

// wrapTransport lets tests inject network faults; nil in production.
var wrapTransport func(http.RoundTripper) http.RoundTripper

// hubConn talks to one Hub, pinning its certificate when the invite carried one.
type hubConn struct {
	base    string
	agent   string
	key     ed25519.PrivateKey
	http    *http.Client
	timeout time.Duration
	// workspaceCheck, when set, gates every request except the version
	// probe on the pinned workspace identity (workspaces_realmguard.go).
	workspaceCheck func(context.Context, string) error
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
	var rt http.RoundTripper = tr
	if wrapTransport != nil {
		rt = wrapTransport(rt)
	}
	return &hubConn{base: base, agent: agent, key: key, http: &http.Client{Transport: rt}, timeout: requestTimeout}, nil
}

// release closes the idle connections to the Hub and stops dials that no
// request waits for any more: net/http keeps dialing after the request that
// started a dial has ended, so the connection would otherwise reach the Hub
// after its owner was closed.
func (c *hubConn) release() {
	if c != nil {
		c.http.CloseIdleConnections()
	}
}

// request builds a request with a raw body, signed unless the connection
// has no agent yet.
func (c *hubConn) request(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	if c.workspaceCheck != nil {
		if err := c.workspaceCheck(ctx, path); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
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
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := c.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, protocol.MaxBody)).Decode(out)
}

// getRange copies bytes [from, to) of path into w within the request timeout.
func (c *hubConn) getRange(ctx context.Context, path string, from, to int64, w io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := c.request(ctx, "GET", path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to-1))
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return err
	}
	if resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("hub ignored range request (%s)", resp.Status)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, to-from))
	if err == nil && n != to-from {
		err = io.ErrUnexpectedEOF
	}
	return err
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode/100 == 2 {
		return nil
	}
	var e protocol.Error
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
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
