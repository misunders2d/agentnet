package client

import (
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

type failingHub func(*http.Request) (*http.Response, error)

func (f failingHub) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Only a Hub that cannot even be dialed yields this device's own stored
// record, marked local; answers, server, TLS and read failures stay errors.
func TestStatusLocalRecordOnlyWhenHubUnreachable(t *testing.T) {
	w := newWorld(t, "")
	sent, err := w.alice.Send(tctx(t), w.bob.Address, "status me", "")
	if err != nil {
		t.Fatal(err)
	}
	stored, _, _, _ := w.alice.store.outboxState(sent.ID)
	base := w.alice.hub.http.Transport
	t.Cleanup(func() { w.alice.hub.http.Transport = base })
	set := func(f failingHub) { w.alice.hub.http.Transport = f }

	set(func(*http.Request) (*http.Response, error) {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("socket: operation not permitted")}
	})
	r, err := w.alice.Status(tctx(t), sent.ID, 0)
	var local *LocalStatus
	if !errors.As(err, &local) || r.ID != sent.ID || r.State != stored || !strings.Contains(err.Error(), "local record only") {
		t.Fatalf("unreachable Hub: %+v %v", r, err)
	}
	if _, err = w.alice.Status(tctx(t), protocol.NewID(), 0); err == nil || errors.As(err, &local) {
		t.Fatalf("unknown id given a local record: %v", err)
	}
	for name, f := range map[string]failingHub{
		"server": func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":"down"}`)), Header: http.Header{}, Request: r}, nil
		},
		"tls": func(*http.Request) (*http.Response, error) { return nil, x509.UnknownAuthorityError{} },
		"read": func(*http.Request) (*http.Response, error) {
			return nil, &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset")}
		},
	} {
		set(f)
		if _, err = w.alice.Status(tctx(t), sent.ID, 0); err == nil || errors.As(err, &local) {
			t.Fatalf("%s failure became a local record: %v", name, err)
		}
	}
	w.alice.hub.http.Transport = base
	if r, err = w.alice.Status(tctx(t), sent.ID, 0); err != nil || r.State == "" {
		t.Fatalf("reachable Hub: %+v %v", r, err)
	}
}
