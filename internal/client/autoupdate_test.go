package client

import (
	"context"
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

	"github.com/misunders2d/agentnet/internal/protocol"
)

const refusalBody = `{"error":"update_required","latest":"v0.8.18","url":"https://github.com/misunders2d/agentnet/releases/tag/v0.8.18","message":"Update AgentNet to v0.8.18 to continue."}`

func refusalResponse(r *http.Request) *http.Response {
	return &http.Response{StatusCode: http.StatusUpgradeRequired, Status: "426 Upgrade Required", Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(refusalBody)), Request: r}
}

// suspendingHub stands in for a Hub that suspends this device until it runs
// v0.8.18 (owner policy "latest only"): while on, its stream sends
// update_required and then nothing (no members, messages or receipts), and
// it refuses everything else but the ping acknowledgement and the version
// probe with 426. Turning it on or off cuts the open streams, as the Hub's
// decision reaches a device when it connects.
type suspendingHub struct {
	base    http.RoundTripper
	mu      sync.Mutex
	on      bool
	open    []io.Closer
	refused atomic.Int64 // requests that reached it while it refused this device
	streams atomic.Int64 // suspended streams it served
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
	allowed := stream || r.Method == "GET" && r.URL.Path == "/v1/version" || r.Method == "POST" && r.URL.Path == "/v1/stream/ack"
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
	// Only a release tag and an https page are kept from what the Hub says.
	if e := updateRequiredError("make me run this", "http://insecure.example/"); e.Latest != "" || e.URL != "" || e.Msg != "Update AgentNet to continue." {
		t.Fatalf("kept %+v", e)
	}
}

// A refusal one process meets is recorded for the others (status, doctor,
// the hook line); a later answered request ends it, at once in that
// process and in the record.
func TestUpdateRequiredRecordedAndServed(t *testing.T) {
	running(t, "v0.8.17")
	w := newWorld(t, "")
	w.bob.hub.gate.after(nil, updateRequiredError("v0.8.18", "https://example.test/r"))
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
	if _, ok := w.bob.UpdateRequired(); ok {
		t.Fatal("still refused after the Hub answered")
	}
	w.bob.required.writes.Wait()
	if _, ok := w.bob.store.updateRequired(); ok {
		t.Fatal("the record outlived the Hub's answer")
	}
	running(t, "v0.8.18") // another version: an old record is over
	w.bob.hub.gate.after(nil, updateRequiredError("v0.8.19", ""))
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
	a.hub.gate.after(nil, updateRequiredError("v0.8.19", "")) // a refusal is a trigger too
	a.maybeAutoUpdate(context.Background())
	eventually(t, "the required release", func() bool { c := got(); return len(c) == 3 && c[2] == "v0.8.19" })
	answer <- nil
	a.auto.runs.Wait()
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
