package client

import (
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/testhub"
)

var errInjected = errors.New("injected network failure")

// faults fails matching requests a set number of times. With afterSend the
// request reaches the Hub and only the response is lost.
type faults struct {
	mu    sync.Mutex
	rules []*fault
}

type fault struct {
	method, pathPart string
	skip, times      int
	afterSend        bool
}

func (f *faults) add(method, pathPart string, times int, afterSend bool) {
	f.addAfter(method, pathPart, 0, times, afterSend)
}

// addAfter lets skip matching requests through before failing times of them.
func (f *faults) addAfter(method, pathPart string, skip, times int, afterSend bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = append(f.rules, &fault{method, pathPart, skip, times, afterSend})
}

func (f *faults) take(r *http.Request) *fault {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rule := range f.rules {
		if rule.times > 0 && r.Method == rule.method && strings.Contains(r.URL.Path, rule.pathPart) {
			if rule.skip > 0 {
				rule.skip--
				continue
			}
			rule.times--
			return rule
		}
	}
	return nil
}

type faultRT struct {
	base http.RoundTripper
	f    *faults
}

func (rt faultRT) RoundTrip(r *http.Request) (*http.Response, error) {
	rule := rt.f.take(r)
	if rule == nil {
		return rt.base.RoundTrip(r)
	}
	if rule.afterSend {
		if resp, err := rt.base.RoundTrip(r); err == nil {
			resp.Body.Close()
		}
	}
	return nil, errInjected
}

func injectFaults(a *Agent) *faults {
	f := &faults{}
	a.hub.http.Transport = faultRT{a.hub.http.Transport, f}
	return f
}

func tctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return c
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

type world struct {
	hub          *testhub.Proc
	alice, bob   *Agent
	bobHome      string
	aliceInvites func(label string) string
}

func newWorld(t *testing.T, publicURL string) *world {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "hub")
	w := &world{hub: testhub.Start(t, dir, "127.0.0.1:0", publicURL)}
	w.alice = mustJoin(t, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "alice")
	w.aliceInvites = func(label string) string {
		code, err := w.alice.Invite(tctx(t), label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	w.bobHome = filepath.Join(t.TempDir(), "bob")
	w.bob = mustJoin(t, w.bobHome, w.aliceInvites("bob"), "laptop")
	return w
}

func mustJoin(t *testing.T, home, code, name string) *Agent {
	t.Helper()
	// Join creates the home (keys, SQLite schema) and then asks the Hub,
	// all within its context: a longer budget than one request's for slow
	// CI disks (Windows).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	a, err := Join(ctx, home, code, name)
	if err != nil {
		t.Fatalf("join %s: %v", name, err)
	}
	t.Cleanup(func() { a.Close() })
	return a
}

func runAgent(t *testing.T, a *Agent) func() {
	t.Helper()
	a.Logf = t.Logf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { a.Run(ctx, RunOptions{}); close(done) }()
	stop := func() { cancel(); <-done }
	t.Cleanup(stop)
	return stop
}

func state(t *testing.T, a *Agent, id string) string {
	r, err := a.Status(tctx(t), id, 0)
	if err != nil {
		return err.Error()
	}
	return r.State
}

func count(t *testing.T, a *Agent, table string) int {
	var n int
	if err := a.store.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// R1: a one-shot directory failure and a one-shot receipt failure are
// recovered by the running daemon without a restart.
func TestDaemonRecoversTransientFailures(t *testing.T) {
	w := newWorld(t, "")
	f := injectFaults(w.bob)
	f.add("GET", "/v1/agents/admin/alice", 1, false) // bob has not pinned alice yet
	f.add("POST", "/ack", 1, true)                   // Hub records nothing, bob sees an error
	runAgent(t, w.bob)
	res, err := w.alice.Send(tctx(t), w.bob.Address, "hello", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "delivered after retries", func() bool { return state(t, w.alice, res.ID) == protocol.StateDelivered })
	if n := count(t, w.bob, "inbox"); n != 1 {
		t.Fatalf("inbox has %d messages", n)
	}
}

// R1: ordinary requests are bounded even if the Hub stops answering.
func TestStalledRequestIsBounded(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	srv.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()
	defer close(release)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	defer func(d time.Duration) { requestTimeout = d }(requestTimeout)
	requestTimeout = 200 * time.Millisecond
	id, _ := identity.Generate()
	conn, err := newHubConn(srv.URL, string(certPEM), "a/b", id.Sign)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err = conn.do(context.Background(), "GET", "/v1/agents/a/b", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("stalled request: err=%v after %s", err, time.Since(start))
	}
	// The push stream must not wait forever for response headers either.
	a := &Agent{Address: "a/b", id: id, hub: conn, heartbeat: time.Hour, Logf: t.Logf}
	start = time.Now()
	if _, err := a.streamOnce(context.Background()); err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("stalled stream: err=%v after %s", err, time.Since(start))
	}
}

// R2: if the Hub's answer to join is lost, the same join can be repeated
// with the same keys; the consumed invite still refuses any other key.
func TestJoinResponseLostThenRetried(t *testing.T) {
	w := newWorld(t, "")
	code := w.aliceInvites("carol")
	home := filepath.Join(t.TempDir(), "carol")

	f := &faults{}
	f.add("POST", "/v1/join", 1, true)
	wrapTransport = func(rt http.RoundTripper) http.RoundTripper { return faultRT{rt, f} }
	_, err := Join(tctx(t), home, code, "desk")
	wrapTransport = nil
	if err == nil {
		t.Fatal("join reported success although the response was lost")
	}
	if _, err := Open(home); err == nil {
		t.Fatal("half-enrolled agent opened")
	}
	carol := mustJoin(t, home, code, "desk")
	if _, err := w.alice.Send(tctx(t), carol.Address, "welcome", ""); err != nil {
		t.Fatalf("send to recovered agent: %v", err)
	}
	if _, err := Join(tctx(t), filepath.Join(t.TempDir(), "mallory"), code, "desk"); err == nil {
		t.Fatal("consumed invite accepted a different key")
	}
}

// R2: keys saved before a crash are reused by the next join.
func TestJoinReusesSavedKeys(t *testing.T) {
	w := newWorld(t, "")
	home := filepath.Join(t.TempDir(), "dave")
	if err := secfile.EnsureDir(home); err != nil {
		t.Fatal(err)
	}
	id, _ := identity.Generate()
	idPath, _ := paths(home)
	if err := id.Save(idPath); err != nil {
		t.Fatal(err)
	}
	dave := mustJoin(t, home, w.aliceInvites("dave"), "desk")
	if dave.Self().Fingerprint() != id.Public(dave.Address).Fingerprint() {
		t.Fatal("join generated new keys instead of reusing the saved ones")
	}
}

// R5 + R3: a sender key change is held (not rejected) even when the first
// directory lookup fails; explicit trust promotes the held messages exactly
// once, and an interrupted trust can be repeated.
func TestKeyChangeHeldThenTrustedAcrossRestart(t *testing.T) {
	w := newWorld(t, "")
	stale, _ := identity.Generate()
	if err := w.bob.store.pin(stale.Public(w.alice.Address)); err != nil { // bob trusted an older alice key
		t.Fatal(err)
	}
	f := injectFaults(w.bob)
	f.add("GET", "/v1/agents/admin/alice", 1, false)
	stop := runAgent(t, w.bob)
	var ids []string
	for _, body := range []string{"one", "two"} {
		res, err := w.alice.Send(tctx(t), w.bob.Address, body, "")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.ID)
	}
	for _, id := range ids {
		eventually(t, "quarantined receipt", func() bool { return state(t, w.alice, id) == protocol.StateQuarantined })
	}
	stop()
	if n := count(t, w.bob, "quarantine"); n != 2 {
		t.Fatalf("quarantine has %d", n)
	}

	f.add("POST", "/ack", 2, false)
	if _, err := w.bob.Trust(tctx(t), w.alice.Address); err == nil {
		t.Fatal("trust hid failed receipts")
	}
	if count(t, w.bob, "inbox") != 2 || count(t, w.bob, "quarantine") != 0 {
		t.Fatal("promotion not durable before receipts")
	}
	w.bob.Close()
	bob, err := Open(w.bobHome) // restart
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	if _, err := bob.Trust(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	if count(t, bob, "inbox") != 2 {
		t.Fatalf("inbox has %d after repeated trust", count(t, bob, "inbox"))
	}
	for _, id := range ids {
		if s := state(t, w.alice, id); s != protocol.StateDelivered {
			t.Fatalf("message %s state %s", id, s)
		}
	}
}

// TrustKey pins only the key the person compared: when the directory holds
// another one by the time they confirm, nothing is pinned or promoted.
func TestTrustKeyPinsOnlyTheComparedKey(t *testing.T) {
	w := newWorld(t, "")
	stale, _ := identity.Generate()
	if err := w.bob.store.pin(stale.Public(w.alice.Address)); err != nil {
		t.Fatal(err)
	}
	stop := runAgent(t, w.bob)
	res, err := w.alice.Send(tctx(t), w.bob.Address, "held until trusted", "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "quarantined", func() bool { return state(t, w.alice, res.ID) == protocol.StateQuarantined })
	stop()
	compared, _ := identity.Generate() // what the person saw; the directory now has alice's real key
	if _, err := w.bob.TrustKey(tctx(t), w.alice.Address, compared.Public(w.alice.Address).Fingerprint()); err == nil ||
		!strings.Contains(err.Error(), "nothing was trusted") {
		t.Fatalf("mismatched trust: %v", err)
	}
	if pinned, _, _, _ := w.bob.store.peer(w.alice.Address); pinned.Fingerprint() != stale.Public(w.alice.Address).Fingerprint() {
		t.Fatal("pin changed")
	}
	if count(t, w.bob, "quarantine") != 1 || count(t, w.bob, "inbox") != 0 {
		t.Fatal("held message promoted")
	}
	fp, err := w.bob.TrustKey(tctx(t), w.alice.Address, w.alice.Self().Fingerprint())
	if err != nil || fp != w.alice.Self().Fingerprint() || count(t, w.bob, "inbox") != 1 {
		t.Fatalf("matching trust: %s %v", fp, err)
	}
}

// R6: a Hub URL with a trailing slash works for join and signed requests.
func TestTrailingSlashHubURL(t *testing.T) {
	w := newWorld(t, "")
	inv, err := protocol.DecodeInvite(w.aliceInvites("erin"))
	if err != nil {
		t.Fatal(err)
	}
	inv.Hub += "/"
	erin := mustJoin(t, filepath.Join(t.TempDir(), "erin"), inv.Encode(), "desk")
	if _, err := erin.Send(tctx(t), w.alice.Address, "hi", ""); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(t.TempDir(), "hub2")
	p := testhub.Start(t, dir, "127.0.0.1:0", "")
	p.Stop()
	p = testhub.Start(t, dir, p.Addr, "https://"+p.Addr+"/")
	admin := mustJoin(t, filepath.Join(t.TempDir(), "admin"), testhub.BootstrapCode(t, dir), "desk")
	if _, err := admin.Invite(tctx(t), "x", time.Hour, false); err != nil {
		t.Fatal(err)
	}
}

// Two people both called Bernard, both on a ThinkPad: the second join is
// refused with a free name to offer, nothing is enrolled, and the same
// invite and keys enroll the address the person confirms. The first
// enrollment is never renamed or replaced.
func TestJoinCollisionKeepsInviteAndKeys(t *testing.T) {
	w := newWorld(t, "")
	smithHome := filepath.Join(t.TempDir(), "smith")
	smith := mustJoin(t, smithHome, w.aliceInvites("bernard"), "thinkpad")
	smithAddr := smith.Address
	smith.Close()

	code := w.aliceInvites("bernard")
	home := filepath.Join(t.TempDir(), "kim")
	_, err := Join(tctx(t), home, code, "thinkpad")
	if !errors.Is(err, ErrAddressTaken) || !strings.Contains(err.Error(), "bernard/thinkpad-2 is free now (not reserved)") ||
		!strings.Contains(err.Error(), "invitation and this computer's key are still valid") {
		t.Fatalf("collision: %v", err)
	}
	if _, err := Open(home); err == nil || !strings.Contains(err.Error(), "with another NAME") {
		t.Fatalf("refused join left an openable agent or unclear advice: %v", err)
	}
	idPath, _ := paths(home)
	before, err := identity.Load(idPath)
	if err != nil {
		t.Fatal(err)
	}
	kim := mustJoin(t, home, code, "thinkpad-2")
	if kim.Address != "bernard/thinkpad-2" || kim.Self().Fingerprint() != before.Public(kim.Address).Fingerprint() {
		t.Fatalf("retry: %s with new keys?", kim.Address)
	}
	again, err := Open(smithHome)
	if err != nil || again.Address != smithAddr {
		t.Fatalf("first enrollment changed: %v %v", err, again)
	}
	again.Close()
	if _, err := Join(tctx(t), smithHome, w.aliceInvites("bernard"), "thinkpad-3"); err == nil {
		t.Fatal("an enrolled home was enrolled again")
	}
	if _, err := w.alice.Send(tctx(t), kim.Address, "welcome", ""); err != nil {
		t.Fatalf("send to the confirmed address: %v", err)
	}
}
