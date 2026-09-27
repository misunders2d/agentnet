// Package testhub runs a real TLS Hub for tests.
package testhub

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/hub"
)

// Proc is a Hub serving on a local port.
type Proc struct {
	Dir, Addr string
	stop      context.CancelFunc
	done      chan struct{}
}

// Start serves a Hub from dir on addr ("127.0.0.1:0" for any port). publicURL
// defaults to https://ADDR. The Hub stops at test cleanup if still running.
func Start(t *testing.T, dir, addr, publicURL string) *Proc {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	p := &Proc{Dir: dir, Addr: ln.Addr().String(), done: make(chan struct{})}
	if publicURL == "" {
		publicURL = "https://" + p.Addr
	}
	h, err := hub.Open(hub.Config{DataDir: dir, PublicURL: publicURL, Logf: t.Logf})
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	go func() {
		defer close(p.done)
		if err := h.Serve(ctx, ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("hub serve: %v", err)
		}
		h.Close()
	}()
	t.Cleanup(p.Stop)
	return p
}

// Stop shuts the Hub down and waits for it; safe to call more than once.
func (p *Proc) Stop() { p.stop(); <-p.done }

// BootstrapCode reads the Hub's first admin invite.
func BootstrapCode(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, hub.BootstrapFile))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}
