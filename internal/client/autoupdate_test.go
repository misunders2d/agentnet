package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
)

const refusalBody = `{"error":"update_required","latest":"v0.8.18","url":"https://github.com/misunders2d/agentnet/releases/tag/v0.8.18","message":"Update AgentNet to v0.8.18 to continue."}`

func refusalResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusUpgradeRequired, Status: "426 Upgrade Required", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(refusalBody)), Request: r}
}

// hubServesSuspended is what the Hub still serves a device that must update
// first (internal/hub/update.go whileSuspended, contract REVISION 2.2): its
// stream, the stream's ping acknowledgements, the version probe and the
// recommendation, receipts, the read-only lookups an answer or a result
// makes first (a member's directory entry, sessions and profile, a
// person's chain), the upload of its files, and posts of messages whose
// outer kind is answer or result.
func hubServesSuspended(r *http.Request) bool {
	p := r.URL.Path
	seg := strings.Split(strings.TrimPrefix(p, "/v1/"), "/")
	switch {
	case r.Method == "GET" && (p == "/v1/stream" || p == "/v1/version" || p == "/v1/release"),
		r.Method == "POST" && p == "/v1/stream/ack",
		r.Method == "POST" && len(seg) == 3 && seg[0] == "messages" && seg[2] == "ack",
		r.Method == "GET" && seg[0] == "agents" && (len(seg) == 3 || len(seg) == 4 && (seg[3] == "sessions" || seg[3] == "profile")),
		r.Method == "GET" && len(seg) == 3 && seg[0] == "persons" && seg[2] == "chain",
		r.Method == "POST" && p == "/v1/blobs",
		(r.Method == "GET" || r.Method == "PUT") && len(seg) == 2 && seg[0] == "blobs",
		r.Method == "POST" && len(seg) == 3 && seg[0] == "blobs" && seg[2] == "complete":
		return true
	case r.Method == "POST" && p == "/v1/messages":
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var env struct {
			Kind string `json:"kind"`
		}
		return json.Unmarshal(body, &env) == nil && (env.Kind == envelope.KindAnswer || env.Kind == envelope.KindResult)
	}
	return false
}

// suspendingHub stands in for a Hub that suspends this device until it runs
// v0.8.18 (owner policy "latest only"): while on, its stream sends
// update_required and then nothing (no members, messages or receipts), and
// it refuses everything but what it serves a suspended device
// (hubServesSuspended) with 426. Turning it on or off cuts the open
// streams, as the Hub's decision reaches a device when it connects.
type suspendingHub struct {
	base    http.RoundTripper
	mu      sync.Mutex
	on      bool
	open    []io.Closer
	refused atomic.Int64 // requests that reached it while it refused this device
	served  atomic.Int64 // requests other than the stream it served while it did
	streams atomic.Int64 // suspended streams it served
	uploads atomic.Int64 // file reservations it served while it refused this device
}

func (s *suspendingHub) set(on bool) {
	s.mu.Lock()
	s.on = on
	open := s.open
	s.open = nil
	s.mu.Unlock()
	for _, c := range open {
		c.Close()
	}
}

func (s *suspendingHub) keep(c io.Closer) {
	s.mu.Lock()
	s.open = append(s.open, c)
	s.mu.Unlock()
}

func (s *suspendingHub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	on := s.on
	s.mu.Unlock()
	stream := r.Method == "GET" && r.URL.Path == "/v1/stream"
	allowed := on && hubServesSuspended(r)
	if allowed && !stream {
		s.served.Add(1)
	}
	if allowed && r.Method == "POST" && r.URL.Path == "/v1/blobs" {
		s.uploads.Add(1)
	}
	switch {
	case on && stream:
		pr, pw := io.Pipe()
		go pw.Write([]byte("event: update_required\ndata: {\"latest\":\"v0.8.18\",\"url\":\"https://github.com/misunders2d/agentnet/releases/tag/v0.8.18\"}\n\n"))
		go func() { <-r.Context().Done(); pr.CloseWithError(r.Context().Err()) }() // as a connection ends with its request
		s.keep(pr)
		s.streams.Add(1)
		return &http.Response{StatusCode: 200, Status: "200 OK", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: pr, Request: r}, nil
	case on && !allowed:
		s.refused.Add(1)
		return refusalResponse(r), nil
	}
	resp, err := s.base.RoundTrip(r)
	if err == nil && stream {
		s.keep(resp.Body)
	}
	return resp, err
}

// A suspended device (the Hub's update_required) records it, says so to
// its coding agents and in doctor, keeps what it sends queued instead of
// failing it, and asks the Hub nothing more however often its retry pass
// runs; once the Hub serves it again (its stream brings the member list),
// what waited is delivered and the record ends.
func TestSuspendedDeviceStopsRequestsUntilServed(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	hub := &suspendingHub{base: w.bob.hub.http.Transport}
	w.bob.hub.http.Transport = hub
	runWith(t, w, w.bob, RunOptions{})
	if _, err := w.bob.Send(tctx(t), w.alice.Address, "before", ""); err != nil { // alice's key pinned here
		t.Fatal(err)
	}
	hub.set(true) // bob's stream is cut; he connects to the suspended one
	eventually(t, "bob is told he is suspended", func() bool { return hub.streams.Load() > 0 && w.bob.hub.gate.holding() })
	if u, ok := w.bob.UpdateRequired(); !ok || u.Latest != "v0.8.18" || u.URL != "https://github.com/misunders2d/agentnet/releases/tag/v0.8.18" || u.Running != "v0.8.17" {
		t.Fatalf("recorded %+v %v", u, ok)
	}
	before := hub.refused.Load()
	res, err := w.bob.Send(tctx(t), w.alice.Address, "while suspended", "")
	if err != nil || res.State != stateQueued {
		t.Fatalf("send while suspended: %+v %v (it must wait, not fail)", res, err)
	}
	for range 3 {
		w.bob.sync(tctx(t)) // the retry pass every ping runs
		w.bob.kickNow()
		w.bob.wakeWorker()
	}
	time.Sleep(100 * time.Millisecond)
	if n := hub.refused.Load(); n != before {
		t.Fatalf("the suspended device kept asking the Hub: %d refused requests after it knew (was %d)", n, before)
	}
	if st, _, _, _ := w.bob.store.outboxState(res.ID); st != stateQueued {
		t.Fatalf("queued message is %q", st)
	}
	if line := shown(t, w.bob, "S", "UserPromptSubmit"); !strings.Contains(line, "Update AgentNet to v0.8.18 to continue") || !strings.Contains(line, "releases/tag/v0.8.18") {
		t.Fatalf("hook line: %q", line)
	}
	if again := shown(t, w.bob, "S", "PostToolUse"); strings.Contains(again, "Update AgentNet") {
		t.Fatalf("told the session twice: %q", again)
	}
	found := false
	for _, c := range w.bob.Doctor(tctx(t)) {
		if c.Name == "suspended" {
			found = !c.OK && strings.Contains(c.Result, "Update AgentNet to v0.8.18 to continue")
		}
	}
	if !found {
		t.Fatal("doctor does not say the device is suspended")
	}

	hub.set(false) // the Hub serves bob again: his next stream brings the member list
	eventually(t, "the queued message goes once bob is served", func() bool {
		st, _, _, _ := w.bob.store.outboxState(res.ID)
		return st == protocol.StateDelivered || st == protocol.StateCustody
	})
	if _, ok := w.bob.UpdateRequired(); ok || w.bob.hub.gate.holding() {
		t.Fatal("the refusal outlived the Hub serving this device")
	}
}

// A 426 update_required refusal holds the daemon's later requests here,
// except the stream, its ping acks and the version probe; a command (no
// daemon) records it without holding its own requests.
func TestUpdateRequiredRefusalHoldsRequests(t *testing.T) {
	running(t, "v0.8.17")
	var reached atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.URL.Path == "/v1/version" {
			w.Write([]byte(`{"version":"v0.8.18","protocol":1}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUpgradeRequired)
		w.Write([]byte(refusalBody))
	}))
	t.Cleanup(srv.Close)
	var told []string
	c := &hubConn{base: srv.URL, http: srv.Client(), timeout: requestTimeout,
		gate: &updateGate{refused: func(he *HubError) { told = append(told, he.Latest) }}}
	err := c.do(tctx(t), "POST", "/v1/messages", map[string]string{}, nil)
	var he *HubError
	if !errors.As(err, &he) || he.Status != 426 || !errors.Is(err, ErrUpdateRequired) || he.Latest != "v0.8.18" || !retryable(err) || !strings.Contains(he.Msg, "v0.8.18") {
		t.Fatalf("refusal: %#v", err)
	}
	c.do(tctx(t), "PUT", "/v1/caps", nil, nil)
	if reached.Load() != 2 || len(told) != 2 {
		t.Fatalf("a command holds nothing: %d reached, told %v", reached.Load(), told)
	}
	c.gate.setHold(true) // the daemon
	for range 3 {
		if err := c.do(tctx(t), "POST", "/v1/messages", map[string]string{}, nil); !errors.Is(err, ErrUpdateRequired) {
			t.Fatalf("held: %v", err)
		}
	}
	if reached.Load() != 2 {
		t.Fatalf("held requests reached the Hub: %d", reached.Load())
	}
	if err := c.do(tctx(t), "GET", "/v1/version", nil, nil); err != nil || reached.Load() != 3 {
		t.Fatalf("version probe: %v, %d", err, reached.Load())
	}
	c.do(tctx(t), "POST", "/v1/stream/ack", protocol.PingAck{Conn: "c"}, nil)
	if reached.Load() != 4 {
		t.Fatalf("ping ack held: %d", reached.Load())
	}
	// What the Hub still takes from a refused device goes (contract
	// REVISION 2.2); nothing else does.
	for _, c := range []struct {
		method, path, body string
		allowed            bool
	}{
		{"GET", "/v1/release", "", true},
		{"POST", "/v1/messages/abc/ack", `{"state":"delivered"}`, true},
		{"GET", "/v1/agents/admin/alice", "", true},
		{"GET", "/v1/agents/admin/alice/sessions", "", true},
		{"GET", "/v1/agents/admin/alice/profile", "", true},
		{"POST", "/v1/messages", `{"v":1,"id":"a","kind":"answer"}`, true},
		{"POST", "/v1/messages", `{"v":1,"id":"r","kind":"result"}`, true},
		{"POST", "/v1/messages", `{"v":1,"id":"q","kind":"question"}`, false},
		{"POST", "/v1/messages", `{"v":1,"id":"m","kind":"message"}`, false},
		{"POST", "/v1/messages", `not json`, false},
		{"GET", "/v1/agents", "", false},
		{"GET", "/v1/agents/admin/alice/caps", "", false},
		{"PUT", "/v1/caps", "{}", false},
		{"GET", "/v1/persons/p1/chain?after=-1", "", true},
		{"GET", "/v1/persons//chain", "", false},
		{"GET", "/v1/persons/p1", "", false},
		{"POST", "/v1/blobs", `{"id":"b"}`, true},
		{"GET", "/v1/blobs/b", "", true},
		{"PUT", "/v1/blobs/b?offset=0", "x", true},
		{"POST", "/v1/blobs/b/complete", "", true},
		{"GET", "/v1/blobs/b/data", "", false},
		{"DELETE", "/v1/blobs/b", "", false},
		{"POST", "/v1/signal", "{}", false},
	} {
		if got := updateAllowed(c.method, c.path, []byte(c.body)); got != c.allowed {
			t.Errorf("%s %s %s: allowed %v, want %v", c.method, c.path, c.body, got, c.allowed)
		}
	}
	// Only a release tag and an https page are kept from what the Hub says.
	if e := updateRequiredError("make me run this", "http://insecure.example/"); e.Latest != "" || e.URL != "" || e.Msg != "Update AgentNet to continue." {
		t.Fatalf("kept %+v", e)
	}
}

// A refusal one process meets is recorded for the others (status, doctor,
// the hook line); an answered request does not end it (the Hub answers a
// refused device some); the stream's member list, sent only to a device the
// Hub serves, ends it, at once in that process and in the record.
func TestUpdateRequiredRecordedAndServed(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	w.bob.hub.gate.after(updateRequiredError("v0.8.18", "https://example.test/r"))
	w.bob.required.writes.Wait()
	other, err := Open(w.bob.home)
	if err != nil {
		t.Fatal(err)
	}
	if u, ok := other.UpdateRequired(); !ok || u.Latest != "v0.8.18" || u.URL != "https://example.test/r" {
		t.Fatalf("another process reads %+v %v", u, ok)
	}
	other.Close()
	if _, err := w.bob.directory(tctx(t), w.alice.Address); err != nil {
		t.Fatal(err)
	}
	w.bob.required.writes.Wait()
	if _, ok := w.bob.UpdateRequired(); !ok {
		t.Fatal("an answered request ended the refusal")
	}
	w.bob.updateServed("members")
	if _, ok := w.bob.UpdateRequired(); ok {
		t.Fatal("still refused after the Hub served this device")
	}
	if _, ok := w.bob.store.updateRequired(); ok {
		t.Fatal("the record outlived the Hub's answer")
	}
	running(t, "v0.8.18") // another version: an old record is over
	w.bob.hub.gate.after(updateRequiredError("v0.8.19", ""))
	w.bob.required.writes.Wait()
	protocol.Version = "v0.8.19"
	if _, ok := w.bob.store.updateRequired(); ok {
		t.Fatal("a refusal of v0.8.18 holds for v0.8.19")
	}
}

// The automatic update: started once per trigger for the newest release
// the Hub names, one at a time, only while no job runs; a failure is
// recorded and tried again at a later trigger (not within the retry
// spacing); the release the Hub requires wins over an older recommendation.
func TestAutoUpdateTriggers(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	a := w.bob
	var mu sync.Mutex
	var calls []string
	answer := make(chan error)
	a.auto.install = func(ctx context.Context, tag string) (string, error) {
		mu.Lock()
		calls = append(calls, tag)
		mu.Unlock()
		if err := <-answer; err != nil {
			return "", err
		}
		return "installed " + tag, nil
	}
	got := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), calls...) }
	look := func() { a.autoUpdateDue(); a.maybeAutoUpdate(context.Background()) }
	look()
	if len(got()) != 0 {
		t.Fatal("updated with nothing named")
	}
	if err := a.saveRelease([]byte(`{"version":"v0.8.18","url":"https://example.test/r"}`)); err != nil {
		t.Fatal(err)
	}
	a.maybeAutoUpdate(context.Background()) // no trigger since the last look
	if len(got()) != 0 {
		t.Fatal("updated without a trigger")
	}
	a.workerLanes.Lock()
	a.workerLanes.jobs = map[string]executionLane{"job": {}}
	a.workerLanes.Unlock()
	look()
	if len(got()) != 0 {
		t.Fatal("updated while a job runs")
	}
	a.workerLanes.Lock()
	a.workerLanes.jobs = nil
	a.workerLanes.Unlock()
	a.maybeAutoUpdate(context.Background()) // the trigger was kept for when the job ended
	eventually(t, "one attempt", func() bool { return len(got()) == 1 })
	look() // while it runs: no second
	answer <- errors.New("v0.8.18 does not match the release checksum")
	a.auto.runs.Wait()
	if c := got(); len(c) != 1 || c[0] != "v0.8.18" {
		t.Fatalf("attempts %v", c)
	}
	if r, ok, err := ReadAutoUpdate(a.home); err != nil || !ok || r.State != AutoUpdateFailed || r.To != "v0.8.18" || r.From != "v0.8.17" || !strings.Contains(r.Detail, "checksum") {
		t.Fatalf("record %+v %v %v", r, ok, err)
	}
	look() // a reconnect's release event right after: not again yet
	if len(got()) != 1 {
		t.Fatal("a failed release was tried again at once")
	}
	old := autoUpdateRetry
	autoUpdateRetry = 0
	t.Cleanup(func() { autoUpdateRetry = old })
	look()
	eventually(t, "the next trigger tries again", func() bool { return len(got()) == 2 })
	answer <- nil
	a.auto.runs.Wait()
	if r, _, _ := ReadAutoUpdate(a.home); r.State != AutoUpdated || r.Detail != "installed v0.8.18" {
		t.Fatalf("record %+v", r)
	}
	a.hub.gate.after(updateRequiredError("v0.8.19", "")) // a refusal is a trigger too
	a.maybeAutoUpdate(context.Background())
	eventually(t, "the required release", func() bool { c := got(); return len(c) == 3 && c[2] == "v0.8.19" })
	answer <- nil
	a.auto.runs.Wait()
}

// While the Hub refuses this build, the automatic update installs the
// release it requires, which lifts the suspension, even when the admin
// recommends a newer one: a recommendation is a notice and may name a
// release not published (yet) for this platform, so it never keeps the
// device suspended; it is the target again once the Hub serves the device.
func TestAutoUpdateRequiredBeforeNewerRecommendation(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	a := w.bob
	var mu sync.Mutex
	var calls []string
	a.auto.install = func(ctx context.Context, tag string) (string, error) {
		mu.Lock()
		calls = append(calls, tag)
		mu.Unlock()
		if tag == "v0.8.19" {
			return "", errors.New("v0.8.19: no release asset for this platform")
		}
		return "installed " + tag, nil
	}
	got := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), calls...) }
	if err := a.saveRelease([]byte(`{"version":"v0.8.19","url":"https://example.test/r"}`)); err != nil {
		t.Fatal(err)
	}
	a.hub.gate.after(updateRequiredError("v0.8.18", ""))
	a.required.writes.Wait()
	if target := a.autoUpdateTarget(); target != "v0.8.18" {
		t.Fatalf("target while refused: %q, want the required v0.8.18", target)
	}
	a.autoUpdateDue()
	a.maybeAutoUpdate(context.Background())
	a.auto.runs.Wait()
	if c := got(); len(c) != 1 || c[0] != "v0.8.18" {
		t.Fatalf("attempts %v, want the required release first", c)
	}
	if r, _, _ := ReadAutoUpdate(a.home); r.State != AutoUpdated || r.To != "v0.8.18" {
		t.Fatalf("record %+v", r)
	}
	a.hub.gate.clear() // the Hub serves this device again
	if target := a.autoUpdateTarget(); target != "v0.8.19" {
		t.Fatalf("target once served: %q, want the recommendation", target)
	}
}

// A development build never updates itself, nor does a home whose person
// turned automatic updates off; an unreadable setting is not on.
func TestAutoUpdateNeverDevelopmentOrOff(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	a := w.bob
	var calls atomic.Int64
	a.auto.install = func(context.Context, string) (string, error) { calls.Add(1); return "", nil }
	if err := a.saveRelease([]byte(`{"version":"v0.8.18","url":"https://example.test/r"}`)); err != nil {
		t.Fatal(err)
	}
	look := func() { a.autoUpdateDue(); a.maybeAutoUpdate(context.Background()); a.auto.runs.Wait() }
	protocol.Version = "v0.8.17-3-gabcdef0"
	look()
	if calls.Load() != 0 || !strings.Contains(a.AutoUpdateWords(), "never updates itself") {
		t.Fatalf("a development build updated: %d", calls.Load())
	}
	protocol.Version = "v0.8.17"
	if err := SetAutoUpdate(a.home, false); err != nil {
		t.Fatal(err)
	}
	look()
	if calls.Load() != 0 || !strings.Contains(a.AutoUpdateWords(), "off") {
		t.Fatalf("updated while off: %d", calls.Load())
	}
	if err := os.WriteFile(filepath.Join(a.home, autoUpdateFile), []byte("maybe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	look()
	if on, err := AutoUpdateOn(a.home); on || err == nil || calls.Load() != 0 {
		t.Fatalf("garbled setting: %v %v, %d", on, err, calls.Load())
	}
	if err := SetAutoUpdate(a.home, true); err != nil {
		t.Fatal(err)
	}
	look()
	if calls.Load() != 1 {
		t.Fatalf("on again: %d", calls.Load())
	}
}

// The release event wakes the running daemon's automatic update.
func TestReleaseEventStartsAutoUpdate(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	got := make(chan string, 4)
	runWith(t, w, w.bob, RunOptions{AutoUpdate: func(ctx context.Context, tag string) (string, error) { got <- tag; return "ok", nil }})
	recommend(t, w.alice, "v0.8.18", "https://example.test/r")
	select {
	case tag := <-got:
		if tag != "v0.8.18" {
			t.Fatalf("installed %q", tag)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the release event did not start the automatic update")
	}
	select {
	case tag := <-got:
		t.Fatalf("a second attempt for %q", tag)
	case <-time.After(300 * time.Millisecond):
	}
}

// A failed attempt is remembered in the home, so the program that starts
// next (the app relaunched by its update helper after a failed install, a
// restarted daemon) does not try the same release again before the retry
// spacing has passed; an attempt handed over but still running this
// version counts as one; each further failure doubles the spacing.
func TestAutoUpdateFailureOutlivesRestart(t *testing.T) {
	running(t, "v0.8.17")
	old := autoUpdateRetry
	autoUpdateRetry = time.Minute
	t.Cleanup(func() { autoUpdateRetry = old })
	w := newWorld(t, "")
	a := w.bob
	if err := a.saveRelease([]byte(`{"version":"v0.8.18","url":"https://example.test/r"}`)); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	var fail atomic.Bool
	fail.Store(true)
	install := func(context.Context, string) (string, error) {
		calls.Add(1)
		if fail.Load() {
			return "", errors.New("apply failed: permission denied")
		}
		return "the AgentNet app updates as a whole", nil
	}
	start := func(x *Agent) { // Run: a start is a trigger
		x.auto.install = install
		x.autoUpdateDue()
		x.maybeAutoUpdate(context.Background())
		x.auto.runs.Wait()
	}
	restart := func() *Agent { // the next program on the same home
		t.Helper()
		b, err := Open(a.home)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { b.Close() })
		return b
	}
	record := func() AutoUpdateRecord {
		t.Helper()
		r, ok, err := ReadAutoUpdate(a.home)
		if err != nil || !ok {
			t.Fatalf("record: %v %v", ok, err)
		}
		return r
	}
	rewrite := func(r AutoUpdateRecord) { // as the home holds it
		t.Helper()
		b, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(a.home, autoUpdateRecordFile), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ago := func(d time.Duration) { r := record(); r.At = time.Now().Add(-d); rewrite(r) }

	start(a)
	if r := record(); calls.Load() != 1 || r.State != AutoUpdateFailed {
		t.Fatalf("first attempt: %d, %+v", calls.Load(), r)
	}
	start(restart())
	if calls.Load() != 1 {
		t.Fatalf("the release that just failed was tried again by the next program: %d attempts", calls.Load())
	}
	ago(90 * time.Second) // past the spacing after one failure
	fail.Store(false)
	start(restart())
	if r := record(); calls.Load() != 2 || r.State != AutoUpdated {
		t.Fatalf("second attempt: %d, %+v", calls.Load(), r)
	}
	// Handed over, but this version starts again (the app's helper could
	// not install it): no new attempt so soon.
	start(restart())
	if calls.Load() != 2 {
		t.Fatalf("an attempt handed over moments ago was repeated: %d", calls.Load())
	}
	r := record()
	r.State, r.Detail = AutoUpdateFailed, "Update failed; the previous app remains at its verified path: permission denied"
	rewrite(r)
	ago(90 * time.Second) // enough after one failure; this is the second
	start(restart())
	if calls.Load() != 2 {
		t.Fatalf("the second failure was retried after one failure's spacing: %d", calls.Load())
	}
	ago(150 * time.Second)
	start(restart())
	if calls.Load() != 3 {
		t.Fatalf("not tried again after the doubled spacing: %d", calls.Load())
	}
}

// A command on a suspended device (doctor here) makes requests the Hub
// still answers a suspended device: the directory lookup, the
// recommendation. Their answers do not mean the Hub serves this device
// again, so the recorded refusal stands, for doctor and for every other
// process (status, inbox, the hook line).
func TestUpdateRequiredOutlivesWhatTheHubStillServes(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	hub := &suspendingHub{base: w.bob.hub.http.Transport}
	w.bob.hub.http.Transport = hub
	hub.set(true)
	if err := w.bob.hub.do(tctx(t), "GET", "/v1/agents", nil, nil); !errors.Is(err, ErrUpdateRequired) {
		t.Fatalf("refused request: %v", err)
	}
	w.bob.required.writes.Wait()
	if _, ok := w.bob.store.updateRequired(); !ok {
		t.Fatal("refusal not recorded")
	}
	checks := w.bob.Doctor(tctx(t))
	w.bob.required.writes.Wait()
	if hub.served.Load() < 2 {
		t.Fatalf("doctor's lookups did not reach the Hub: %d", hub.served.Load())
	}
	suspended := false
	for _, c := range checks {
		if c.Name == "suspended" {
			suspended = !c.OK && strings.Contains(c.Result, "Update AgentNet to v0.8.18 to continue")
		}
	}
	if !suspended {
		t.Fatalf("doctor does not say the device is suspended: %+v", checks)
	}
	if _, ok := w.bob.store.updateRequired(); !ok {
		t.Fatal("an answered lookup erased the recorded refusal")
	}
	if u, ok := LocalUpdateRequired(w.bob.home); !ok || u.Latest != "v0.8.18" {
		t.Fatalf("another process reads %+v %v", u, ok)
	}
}

// A suspended daemon still finishes admitted work the Hub takes from it
// (contract REVISION 2.2): receipts of what it received and the answer to
// a question it holds go, an answer's files with it; new work stays queued
// without reaching the Hub, its files too (no retry pass uploads them);
// neither ends the suspension.
func TestSuspendedDeviceDrainsAdmittedWork(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	hub := &suspendingHub{base: w.bob.hub.http.Transport}
	w.bob.hub.http.Transport = hub
	runWith(t, w, w.bob, RunOptions{})
	if _, err := w.bob.Send(tctx(t), w.alice.Address, "before", ""); err != nil { // alice's key pinned here
		t.Fatal(err)
	}
	q, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "what is the plan?", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	q2, err := w.alice.SendMessage(tctx(t), Outgoing{To: w.bob.Address, Body: "send me the plan as a file", Kind: envelope.KindQuestion})
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "bob holds the questions", func() bool {
		_, kind, err := w.bob.store.inboxKind(q.ID)
		_, kind2, err2 := w.bob.store.inboxKind(q2.ID)
		return err == nil && kind == envelope.KindQuestion && err2 == nil && kind2 == envelope.KindQuestion
	})
	file := filepath.Join(t.TempDir(), "plan.txt")
	if err := os.WriteFile(file, []byte("the plan, in a file"), 0o600); err != nil {
		t.Fatal(err)
	}
	hub.set(true)
	eventually(t, "bob is told he is suspended", func() bool { return hub.streams.Load() > 0 && w.bob.hub.gate.holding() })
	if err := w.bob.store.resendReceipt(q.ID); err != nil { // a receipt the Hub has not recorded yet
		t.Fatal(err)
	}
	before := hub.refused.Load()
	plain, err := w.bob.Send(tctx(t), w.alice.Address, "new work", "")
	if err != nil || plain.State != stateQueued {
		t.Fatalf("new work while suspended: %+v %v (it waits)", plain, err)
	}
	uploads := hub.uploads.Load()
	withFile, err := w.bob.Send(tctx(t), w.alice.Address, "new work with a file", "", file)
	if err != nil || withFile.State != stateQueued {
		t.Fatalf("new work with a file while suspended: %+v %v (it waits)", withFile, err)
	}
	if n := hub.uploads.Load(); n != uploads {
		t.Fatalf("held work's file reached the Hub: %d uploads (was %d)", n, uploads)
	}
	ans, err := w.bob.Reply(tctx(t), q.ID, "the plan is to update")
	if err != nil || ans.State != protocol.StateCustody && ans.State != protocol.StateDelivered {
		t.Fatalf("the answer to admitted work: %+v %v (the Hub takes it)", ans, err)
	}
	ans2, err := w.bob.Reply(tctx(t), q2.ID, "the plan is attached", file)
	if err != nil || ans2.State != protocol.StateCustody && ans2.State != protocol.StateDelivered {
		t.Fatalf("the answer with a file to admitted work: %+v %v (the Hub takes it and its file)", ans2, err)
	}
	if hub.uploads.Load() != uploads+1 {
		t.Fatalf("the answer's file: %d uploads (was %d)", hub.uploads.Load(), uploads)
	}
	uploads = hub.uploads.Load()
	for range 3 {
		w.bob.sync(tctx(t)) // the retry pass a ping runs
	}
	if pending, err := w.bob.store.unsentReceipts(); err != nil || len(pending) != 0 {
		t.Fatalf("receipts still unsent while suspended: %v %v", pending, err)
	}
	for _, id := range []string{plain.ID, withFile.ID} {
		if st, _, _, _ := w.bob.store.outboxState(id); st != stateQueued {
			t.Fatalf("new work %s is %q", id, st)
		}
	}
	if n := hub.refused.Load(); n != before {
		t.Fatalf("held work reached the Hub: %d refused (was %d)", n, before)
	}
	if n := hub.uploads.Load(); n != uploads {
		t.Fatalf("held work's file reached the Hub: %d uploads (was %d)", n, uploads)
	}
	w.bob.required.writes.Wait()
	if _, ok := w.bob.UpdateRequired(); !ok || !w.bob.hub.gate.holding() {
		t.Fatal("what the Hub still takes from a suspended device ended the suspension")
	}
	if _, ok := w.bob.store.updateRequired(); !ok {
		t.Fatal("the recorded refusal was erased")
	}
}

// A suspended daemon's agent finishes a request it admitted before in a
// conversation with an external participation: the reply first refreshes
// each member's person (GET /v1/persons/{id}/chain), which the Hub still
// serves a suspended device (contract REVISION 2.2), so the reply is
// stored and goes; the request never ends "not delivered" because the
// device must update.
func TestSuspendedDeviceFinishesConversationReply(t *testing.T) {
	running(t, "v0.8.17")
	w, host, conv, _, _, records, stopHost := externalAgentWorld(t)
	p, err := w.alice.InviteNamedAgent(tctx(t), conv, host.Address, records[0].ID, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "external invitation", func() bool { return stateAt(t, host, p.PID).State == PartInvited })
	if _, err := host.AcceptParticipation(tctx(t), p.PID); err != nil {
		t.Fatal(err)
	}
	for _, a := range []*Agent{w.alice, w.bob} {
		eventually(t, "accepted external identity", func() bool { return stateAt(t, a, p.PID).Claimable() })
	}
	stopHost()
	q, err := w.alice.AskAgent(tctx(t), p.PID, envelope.KindQuestion, "what is the plan?")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := w.alice.store.db.Query(`SELECT envelope FROM outbox WHERE recipient=? AND kind=?`, host.Address, envelope.KindQuestion)
	if err != nil {
		t.Fatal(err)
	}
	var copies []envelope.Envelope
	for rows.Next() {
		var raw string
		var env envelope.Envelope
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			t.Fatal(err)
		}
		copies = append(copies, env)
	}
	rows.Close()
	for _, env := range copies {
		if err := host.accept(tctx(t), env); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "the host holds it", func() bool { return inboxCount(t, host, `id=? AND state=?`, q.ID, stateAgentWaiting) == 1 })
	j, found, _, _, err := host.store.claimAgentPage("", host.Address, host.Self().Fingerprint(), 0, agentPage, func(qq dbq, id string) (*ExecutorStamp, error) { return host.ResolveExecutorIn(qq, id, nil) })
	if err != nil || !found || j.ID != q.ID {
		t.Fatalf("claim %+v %v %v", j, found, err)
	}
	host.hub.gate.setHold(true) // as the daemon does once the Hub suspends it
	host.hub.gate.after(updateRequiredError("v0.8.18", ""))
	host.required.writes.Wait()
	host.finishAgent(tctx(t), j, &j.Executor.Responder, envelope.StatusDone, "the plan\nemotion: calm")
	m, err := host.store.inboxMessage(q.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != stateAnswered {
		t.Fatalf("the admitted request ended %q (%s), want answered", m.State, m.Detail)
	}
	var stored int
	if err := host.store.db.QueryRow(`SELECT count(*) FROM outbox WHERE pid=? AND origin LIKE 'agent:%'`, p.PID).Scan(&stored); err != nil || stored == 0 {
		t.Fatalf("the reply was not stored: %d %v", stored, err)
	}
	if !host.hub.gate.holding() {
		t.Fatal("the reply ended the suspension")
	}
}
