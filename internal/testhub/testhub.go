// Package testhub runs a real TLS Hub for tests.
package testhub

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Proc is a Hub serving on a local port.
type Proc struct {
	Dir, Addr string
	Hub       *hub.Hub
	stop      context.CancelFunc
	done      chan struct{}

	mu       sync.Mutex
	requests []string    // "AGENT METHOD /path" of every request, in arrival order
	arrived  []time.Time // when each arrived
	hold     func(*http.Request)
}

// SetHold makes the Hub call hold with every later request before handling
// it, so a test may keep requests waiting; nil: none.
func (p *Proc) SetHold(hold func(*http.Request)) {
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
}

// Requests returns "AGENT METHOD /path" (the address the request says it is
// from, no query) of every request the Hub has received so far, in arrival
// order.
func (p *Proc) Requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests)
}

// Arrivals returns when each of Requests arrived, in the same order.
func (p *Proc) Arrivals() []time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.arrived)
}

// relayDelay is AGENTNET_TEST_RELAY_DELAY: each request waits that long
// before the Hub handles it, as on a relay that far away ("100ms"); with a
// leading "~" each waits a random time up to it ("~600ms"). Unset: none.
func relayDelay(t *testing.T) func() time.Duration {
	v := os.Getenv("AGENTNET_TEST_RELAY_DELAY")
	if v == "" {
		return nil
	}
	random := strings.HasPrefix(v, "~")
	d, err := time.ParseDuration(strings.TrimPrefix(v, "~"))
	if err != nil || d <= 0 {
		t.Fatalf("AGENTNET_TEST_RELAY_DELAY=%q: want a duration such as 100ms or ~600ms", v)
	}
	if random {
		return func() time.Duration { return rand.N(d) }
	}
	return func() time.Duration { return d }
}

func (p *Proc) wrap(delay func() time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.Header.Get(protocol.HeaderAgent)+" "+r.Method+" "+r.URL.Path)
		p.arrived = append(p.arrived, time.Now())
		hold := p.hold
		p.mu.Unlock()
		if hold != nil {
			hold(r)
		}
		if delay != nil {
			select {
			case <-time.After(delay()):
			case <-r.Context().Done():
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// Start serves a Hub from dir on addr ("127.0.0.1:0" for any port). publicURL
// defaults to https://ADDR. The Hub stops at test cleanup if still running.
func Start(t *testing.T, dir, addr, publicURL string) *Proc {
	return StartConfig(t, hub.Config{DataDir: dir, PublicURL: publicURL}, addr)
}

// StartConfig is Start with explicit Hub configuration.
func StartConfig(t *testing.T, cfg hub.Config, addr string) *Proc {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	p := &Proc{Dir: cfg.DataDir, Addr: ln.Addr().String(), done: make(chan struct{})}
	if cfg.PublicURL == "" {
		cfg.PublicURL = "https://" + p.Addr
	}
	if cfg.Logf == nil {
		cfg.Logf = t.Logf
	}
	if cfg.Version == "" {
		// A relay in the test's own process is a development build, whatever
		// version the test's clients say they run (protocol.Version): it
		// recommends nothing by itself and suspends nobody.
		cfg.Version = "dev"
	}
	delay, wrap := relayDelay(t), cfg.Wrap
	cfg.Wrap = func(next http.Handler) http.Handler {
		if wrap != nil {
			next = wrap(next)
		}
		return p.wrap(delay, next)
	}
	h, err := hub.Open(cfg)
	if err != nil {
		ln.Close()
		t.Fatal(err)
	}
	p.Hub = h
	ctx, cancel := context.WithCancel(context.Background())
	p.stop = cancel
	go func() {
		defer close(p.done)
		if err := h.Serve(ctx, ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("hub serve: %v", err)
		}
		if err := h.Close(); err != nil {
			t.Errorf("hub close: %v", err)
		}
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
