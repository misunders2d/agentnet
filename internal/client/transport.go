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

// retryable reports whether a failed call may succeed later unchanged.
func retryable(err error) bool {
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

// request builds a request, signed unless the connection has no agent yet.
func (c *hubConn) request(ctx context.Context, method, path string, in any) (*http.Request, error) {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.agent != "" {
		protocol.SignRequest(req, c.agent, c.key, body)
	}
	return req, nil
}

func (c *hubConn) do(ctx context.Context, method, path string, in, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := c.request(ctx, method, path, in)
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
