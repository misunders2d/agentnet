package client

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
)

// enrolledAt opens an agent whose Hub is at url, pinned to certPEM.
func enrolledAt(t *testing.T, url, certPEM string) *Agent {
	t.Helper()
	home := t.TempDir()
	idPath, dbPath := paths(home)
	id, err := identity.Generate()
	if err == nil {
		err = id.Save(idPath)
	}
	if err != nil {
		t.Fatal(err)
	}
	st, err := openStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	err = st.setConfig(map[string]string{"enrolled": "1", "address": "member/device", "hub": url, "hub_cert": certPEM})
	st.db.Close()
	if err != nil {
		t.Fatal(err)
	}
	a, err := Open(home)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// A closed agent keeps nothing open toward its Hub: not the connection its
// requests used, and not a dial that net/http carries on after the request
// that started it has ended. Such a dial once reached a Hub just as it was
// stopping, missed the stop, and kept the Hub's shutdown waiting until it
// failed (TestLiveResponderControl in internal/ui).
func TestCloseReleasesHubConnections(t *testing.T) {
	// Only Close can end the connection within this bound: the transport
	// itself gives up a handshake after twice as long and drops an idle
	// connection later still.
	bound := func(a *Agent) time.Duration {
		tr := a.hub.http.Transport.(*http.Transport)
		if tr.IdleConnTimeout <= tr.TLSHandshakeTimeout {
			t.Fatalf("transport timeouts changed: %+v", tr)
		}
		return tr.TLSHandshakeTimeout / 2
	}
	t.Run("idle connection", func(t *testing.T) {
		closed := make(chan struct{})
		var once sync.Once
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("{}"))
		}))
		srv.EnableHTTP2 = true // as the Hub serves
		srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
			if s == http.StateClosed {
				once.Do(func() { close(closed) })
			}
		}
		srv.StartTLS()
		defer srv.Close()
		a := enrolledAt(t, srv.URL, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})))
		if err := a.hub.do(tctx(t), "GET", "/v1/version", nil, nil); err != nil {
			t.Fatal(err)
		}
		wait := bound(a)
		a.Close()
		select {
		case <-closed:
		case <-time.After(wait):
			t.Fatal("the agent's connection stayed open after Close")
		}
	})
	t.Run("dial of an ended request", func(t *testing.T) {
		// This Hub accepts and never answers the handshake, so the dial
		// stays in progress until something stops it.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		accepted := make(chan net.Conn, 1)
		go func() {
			if c, err := ln.Accept(); err == nil {
				accepted <- c
			}
		}()
		a := enrolledAt(t, "https://"+ln.Addr().String(), "")
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ended := make(chan error, 1)
		go func() { ended <- a.hub.do(ctx, "GET", "/v1/version", nil, nil) }()
		var conn net.Conn
		select {
		case conn = <-accepted:
		case <-time.After(10 * time.Second):
			t.Fatal("the agent never dialed")
		}
		defer conn.Close()
		hungUp := make(chan struct{})
		go func() { io.Copy(io.Discard, conn); close(hungUp) }() // returns when the agent closes its end
		cancel()
		if err := <-ended; !errors.Is(err, context.Canceled) {
			t.Fatalf("request: %v", err)
		}
		wait := bound(a)
		a.Close()
		select {
		case <-hungUp:
		case <-time.After(wait):
			t.Fatal("the dial went on after Close")
		}
	})
}
