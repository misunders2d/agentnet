package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// stopListener holds the TLS handshake of its second connection until
// open is closed, and reports when that connection arrives and when the
// Hub closes the listener.
type stopListener struct {
	net.Listener
	n                    int
	open, second, closed chan struct{}
	once                 sync.Once
}

func (l *stopListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.n++; l.n == 2 { // Serve accepts on one goroutine
		close(l.second)
		return &gatedConn{Conn: c, open: l.open}, nil
	}
	return c, nil
}

func (l *stopListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

type gatedConn struct {
	net.Conn
	open chan struct{}
}

func (c *gatedConn) Read(p []byte) (int, error) {
	<-c.open
	return c.Conn.Read(p)
}

// h2Frame reads one HTTP/2 frame and returns its type and flags.
func h2Frame(c net.Conn) (byte, byte, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(c, hdr[:]); err != nil {
		return 0, 0, err
	}
	_, err := io.CopyN(io.Discard, c, int64(hdr[0])<<16|int64(hdr[1])<<8|int64(hdr[2]))
	return hdr[3], hdr[4], err
}

// A connection whose TLS handshake is still running when the Hub starts to
// stop must not hold the stop: net/http tells only the HTTP/2 connections
// already served to finish, so this one joined afterwards, was never told,
// and the Hub's shutdown waited for it until it failed.
func TestStopClosesConnectionThatJoinsLate(t *testing.T) {
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := &stopListener{Listener: inner, open: make(chan struct{}), second: make(chan struct{}), closed: make(chan struct{})}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx, ln) }()

	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(h.certPEM))
	// connect opens an HTTP/2 connection: TLS, then the client preface and
	// an empty SETTINGS frame.
	connect := func(raw net.Conn) (net.Conn, error) {
		c := tls.Client(raw, &tls.Config{RootCAs: roots, ServerName: "127.0.0.1", NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS13})
		if err := c.Handshake(); err != nil {
			return nil, err
		}
		_, err := c.Write([]byte("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n\x00\x00\x00\x04\x00\x00\x00\x00\x00"))
		return c, err
	}
	raw, err := net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	served1, err := connect(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer served1.Close()
	if err := served1.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if typ, _, err := h2Frame(served1); err != nil || typ != 0x4 {
		t.Fatalf("first frame %d: %v", typ, err)
	}
	// Server SETTINGS can arrive before net/http reads the client preface
	// and calls StateActive. Wait for its SETTINGS ACK: the first connection
	// is now serving and must receive GOAWAY rather than a late-join close.
	for {
		typ, flags, err := h2Frame(served1)
		if err != nil {
			t.Fatal(err)
		}
		if typ == 0x4 && flags&0x1 != 0 {
			break
		}
	}
	raw, err = net.Dial("tcp", inner.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	late := make(chan net.Conn, 1)
	go func() {
		// Shutdown intentionally closes this late connection; its TLS or
		// preface write may fail. Only the Hub's bounded stop is required.
		c, _ := connect(raw)
		late <- c
	}()
	<-ln.second // accepted, its handshake held

	stop()
	<-ln.closed
	for { // the Hub has told the connections it serves to finish
		typ, _, err := h2Frame(served1)
		if err != nil {
			t.Fatal(err)
		}
		if typ == 0x7 { // GOAWAY
			break
		}
	}
	served1.Close()
	close(ln.open) // the late connection joins now, after that word
	if c := <-late; c != nil {
		defer c.Close() // open until the Hub has stopped
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("stop: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the Hub did not stop")
	}
}
