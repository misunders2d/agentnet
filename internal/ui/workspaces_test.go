package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestWorkspaceProviderRoutingAndStaleHandles(t *testing.T) {
	a, b := NewFixture(time.Now), NewFixture(time.Now)
	set := NewWorkspaceProviders()
	ba, err := set.Bind(client.Workspace{ID: "default", Name: "Same", Endpoint: "https://one.example", State: "enrolled"}, a)
	if err != nil {
		t.Fatal(err)
	}
	bb, err := set.Bind(client.Workspace{ID: strings.Repeat("b", 32), Name: "Same", Endpoint: "https://two.example", State: "enrolled"}, b)
	if err != nil {
		t.Fatal(err)
	}
	var h http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	defer ts.Close()
	s := New(a, strings.TrimPrefix(ts.URL, "http://"), testToken)
	h = s.WorkspaceHandler(set)
	for _, binding := range []WorkspaceBinding{ba, bb} {
		path := "/workspaces/" + binding.ID + "/" + binding.Handle + "/api/overview"
		if got := do(t, ts, "GET", path, "", nil).StatusCode; got != 401 {
			t.Fatalf("scope bypassed cookie: %d", got)
		}
		response := do(t, ts, "GET", path, "", authed(ts, nil))
		if response.StatusCode != 200 {
			t.Fatalf("bound provider: %d", response.StatusCode)
		}
		var o Overview
		if err = json.NewDecoder(response.Body).Decode(&o); err != nil {
			t.Fatal(err)
		}
	}
	set.Remove(ba.ID)
	if got := do(t, ts, "GET", "/workspaces/"+ba.ID+"/"+ba.Handle+"/api/overview", "", authed(ts, nil)).StatusCode; got != 409 {
		t.Fatalf("stale handle: %d", got)
	}
	fresh, err := set.Bind(ba.Workspace, a)
	if err != nil || fresh.Handle == ba.Handle {
		t.Fatal("reused generation handle")
	}
	if got := do(t, ts, "GET", "/workspaces/"+bb.ID+"/"+bb.Handle+"/api/overview", "", authed(ts, nil)).StatusCode; got != 200 {
		t.Fatalf("workspace B broken by A: %d", got)
	}
	if got := do(t, ts, "POST", "/workspaces/"+bb.ID+"/"+bb.Handle+"/api/send", "{}", authed(ts, map[string]string{"Origin": "https://foreign.example", "Content-Type": "application/json"})).StatusCode; got != 403 {
		t.Fatalf("cross-origin allowed: %d", got)
	}
}

// workspaceWorld: this installation's default home on one server, a second
// server whose admin invites it, and the page's workspace routes over a real
// registry and runtime.
func workspaceWorld(t *testing.T) (ts *httptest.Server, invite func(label string) string) {
	t.Helper()
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	one, two := filepath.Join(t.TempDir(), "one"), filepath.Join(t.TempDir(), "two")
	testhub.Start(t, one, "127.0.0.1:0", "")
	testhub.Start(t, two, "127.0.0.1:0", "")
	home := filepath.Join(t.TempDir(), "home")
	a, err := client.Join(ctx, home, testhub.BootstrapCode(t, one), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
	admin, err := client.Join(ctx, filepath.Join(t.TempDir(), "admin"), testhub.BootstrapCode(t, two), "admin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	registry, err := client.OpenWorkspaces(home)
	if err != nil {
		t.Fatal(err)
	}
	set := NewWorkspaceProviders()
	run, stop := context.WithCancel(context.Background())
	runtime := NewWorkspaceRuntime(run, registry, set)
	t.Cleanup(func() { stop(); runtime.Close() })
	var h http.Handler
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	h = New(NewFixture(time.Now), strings.TrimPrefix(ts.URL, "http://"), testToken).WorkspaceHandler(set)
	invite = func(label string) string {
		code, err := admin.Invite(ctx, label, time.Hour, false)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	return ts, invite
}

func workspaceCall(t *testing.T, ts *httptest.Server, method, path string, body any, into any) int {
	t.Helper()
	raw := ""
	hdr := authed(ts, nil)
	if body != nil {
		data, _ := json.Marshal(body)
		raw, hdr = string(data), post(ts)
	}
	resp := do(t, ts, method, path, raw, hdr)
	defer resp.Body.Close()
	if into != nil {
		json.NewDecoder(resp.Body).Decode(into)
	}
	return resp.StatusCode
}

// BUG-40d: a device or workspace name that can never enroll gets a clear
// refusal, not "cannot connect, retry".
func TestWorkspaceJoinBadNamesRefusedClearly(t *testing.T) {
	ts, invite := workspaceWorld(t)
	code := invite("member")
	for _, c := range []struct{ name, agent, says string }{
		{"Other", "Laptop!", "device name"},
		{"Other", "", "device name"},
		{"\u0007" + strings.Repeat("N", 400), "laptop", "workspace name"},
	} {
		var body map[string]string
		status := workspaceCall(t, ts, "POST", "/api/workspaces/join", WorkspaceJoin{Name: c.name, Invite: code, Agent: c.agent}, &body)
		if status != http.StatusBadRequest || !strings.Contains(body["error"], c.says) || body["retry_id"] != "" {
			t.Fatalf("join %q as %q: %d %v", c.name, c.agent, status, body)
		}
	}
	var list []WorkspaceBinding
	if workspaceCall(t, ts, "GET", "/api/workspaces", nil, &list); len(list) != 0 {
		t.Fatalf("refused joins listed: %+v", list)
	}
}

// BUG-19: a disconnected workspace stays listed with its state and can be
// reconnected; joining it again says so instead of "cannot connect, retry".
func TestWorkspaceReconnectAfterDisconnect(t *testing.T) {
	ts, invite := workspaceWorld(t)
	code := invite("member")
	var joined WorkspaceBinding
	if status := workspaceCall(t, ts, "POST", "/api/workspaces/join", WorkspaceJoin{Name: "Other", Invite: code, Agent: "laptop"}, &joined); status != 200 || joined.State != "enrolled" {
		t.Fatalf("join: %d %+v", status, joined)
	}
	if status := workspaceCall(t, ts, "POST", "/api/workspaces/disconnect", map[string]string{"id": joined.ID, "handle": joined.Handle}, nil); status != 200 {
		t.Fatalf("disconnect: %d", status)
	}
	var all []WorkspaceBinding
	if status := workspaceCall(t, ts, "GET", "/api/workspaces/all", nil, &all); status != 200 {
		t.Fatalf("all workspaces: %d", status)
	}
	listed := false
	for _, w := range all {
		if w.ID == joined.ID {
			listed = w.State == "disconnected" && w.Handle == "" && w.Name == "Other" && w.Address == joined.Address
		}
	}
	if !listed {
		t.Fatalf("disconnected workspace not listed with its state: %+v", all)
	}
	// GET /api/workspaces is the list of mounted memberships, each with its
	// handle (the shell registers every one as connected): a disconnected
	// one is listed only by GET /api/workspaces/all.
	var mounted []WorkspaceBinding
	if status := workspaceCall(t, ts, "GET", "/api/workspaces", nil, &mounted); status != 200 {
		t.Fatalf("mounted workspaces: %d", status)
	}
	for _, w := range mounted {
		if w.ID == joined.ID || w.Handle == "" || w.State != "enrolled" {
			t.Fatalf("the mount list names a disconnected or unrouted workspace: %+v", mounted)
		}
	}
	var refused map[string]string
	status := workspaceCall(t, ts, "POST", "/api/workspaces/join", WorkspaceJoin{ID: joined.ID, Name: "Other", Invite: code, Agent: "laptop"}, &refused)
	if status != http.StatusConflict || !strings.Contains(refused["error"], "Reconnect") {
		t.Fatalf("joining a disconnected workspace again: %d %v", status, refused)
	}
	var back WorkspaceBinding
	if status := workspaceCall(t, ts, "POST", "/api/workspaces/reconnect", map[string]string{"id": joined.ID}, &back); status != 200 || back.State != "enrolled" || back.Handle == "" || back.Handle == joined.Handle || back.Address != joined.Address {
		t.Fatalf("reconnect: %d %+v", status, back)
	}
	if status := workspaceCall(t, ts, "GET", "/workspaces/"+back.ID+"/"+back.Handle+"/api/overview", nil, nil); status != 200 {
		t.Fatalf("reconnected workspace not routed: %d", status)
	}
	if status := workspaceCall(t, ts, "POST", "/api/workspaces/reconnect", map[string]string{"id": joined.ID}, nil); status != http.StatusNotFound {
		t.Fatalf("reconnecting a connected workspace: %d", status)
	}
}

func TestWorkspaceJoinSafeFailuresRetainRetryID(t *testing.T) {
	id := strings.Repeat("d", 32)
	valid := protocol.Invite{Hub: "https://relay.example", Label: "member", Secret: strings.Repeat("e", 64)}.Encode()
	cases := []struct {
		code   string
		err    error
		status int
	}{
		{"damaged", &client.HubError{Status: 403, Msg: "PRIVATE_SECRET"}, 400},
		{valid, &client.HubError{Status: 403, Msg: "PRIVATE_SECRET reused expired"}, 403},
		{valid, client.ErrAddressTaken, 409},
	}
	for _, c := range cases {
		set := NewWorkspaceProviders()
		set.Join = func(r *http.Request, b WorkspaceJoin) (client.Workspace, Provider, error) {
			return client.Workspace{ID: id, State: "joining"}, nil, c.err
		}
		server := New(NewFixture(time.Now), "127.0.0.1:12345", testToken)
		raw, _ := json.Marshal(WorkspaceJoin{Name: "Other", Invite: c.code, Agent: "laptop"})
		req := httptest.NewRequest("POST", "http://127.0.0.1:12345/api/workspaces/join", strings.NewReader(string(raw)))
		req.Header.Set("Cookie", cookieName+"="+testToken)
		req.Header.Set("Origin", "http://127.0.0.1:12345")
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.WorkspaceHandler(set).ServeHTTP(rec, req)
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if rec.Code != c.status || body["retry_id"] != id || body["error"] == "" || strings.Contains(rec.Body.String(), "PRIVATE_SECRET") || strings.Contains(rec.Body.String(), valid) {
			t.Fatalf("unsafe or unhelpful failure %d %s", rec.Code, rec.Body.String())
		}
	}
}

// The route uses these provider-owned channels without adding a waiter per mount.
type workspacePushProvider struct {
	Provider
	mu       sync.Mutex
	view     client.ViewChange
	wake     chan struct{}
	observed chan uint64
}

func (p *workspacePushProvider) ViewChanges() (client.ViewChange, <-chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	select {
	case p.observed <- p.view.Seq:
	default:
	}
	return p.view, p.wake
}
func (p *workspacePushProvider) bump(kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.view.Seq++
	switch kind {
	case "typing":
		p.view.Typing = p.view.Seq
	case "presence":
		p.view.Presence = p.view.Seq
	default:
		p.view.Content = p.view.Seq
	}
	close(p.wake)
	p.wake = make(chan struct{})
}
func (p *workspacePushProvider) seen(t *testing.T, seq uint64) {
	t.Helper()
	for {
		select {
		case got := <-p.observed:
			if got >= seq {
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("provider channel was not rearmed")
		}
	}
}
func TestWorkspaceEventsAuthenticatedContentAndMembership(t *testing.T) {
	a := &workspacePushProvider{Provider: NewFixture(time.Now), wake: make(chan struct{}), observed: make(chan uint64, 20)}
	set := NewWorkspaceProviders()
	first, err := set.Bind(client.Workspace{ID: "default", State: "enrolled", Name: "PRIVATE name", Endpoint: "https://private.example", Address: "private/device"}, a)
	if err != nil {
		t.Fatal(err)
	}
	var handler http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	defer ts.Close()
	server := New(a, strings.TrimPrefix(ts.URL, "http://"), testToken)
	handler = server.WorkspaceHandler(set)
	for _, headers := range []map[string]string{nil, {"Cookie": "agentnet_ui=" + testToken, "Origin": "https://foreign.example"}} {
		req, _ := http.NewRequest("GET", ts.URL+"/api/workspaces/events", nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 401 && res.StatusCode != 403 {
			t.Fatal("unguarded workspace events", res.StatusCode)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/workspaces/events", nil)
	req.Header.Set("Cookie", "agentnet_ui="+testToken)
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("workspace stream status=%d", res.StatusCode)
	}
	scanner := bufio.NewScanner(res.Body)
	next := func() (string, map[string]any) {
		t.Helper()
		name := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				name = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				var data map[string]any
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
					t.Fatal(err)
				}
				raw := fmt.Sprint(data)
				if strings.Contains(raw, "PRIVATE") || strings.Contains(raw, "private") {
					t.Fatal("private metadata leaked")
				}
				return name, data
			}
		}
		t.Fatal("missing stream event", scanner.Err())
		return "", nil
	}
	name, data := next()
	if name != "workspaces" || data["rev"] != float64(1) {
		t.Fatal(name, data)
	}
	binding := data["bindings"].([]any)[0].(map[string]any)
	if binding["id"] != "default" || binding["handle"] != first.Handle || binding["seq"] != float64(0) || len(binding) != 3 {
		t.Fatal(binding)
	}
	a.seen(t, 0)
	a.bump("typing")
	a.seen(t, 1)
	a.bump("presence")
	a.seen(t, 2)
	a.bump("")
	name, data = next()
	if name != "change" || data["seq"] != float64(3) || data["handle"] != first.Handle {
		t.Fatal("typing/presence became content", name, data)
	}
	generic := NewFixture(time.Now)
	second, err := set.Bind(client.Workspace{ID: strings.Repeat("b", 32), State: "enrolled"}, generic)
	if err != nil {
		t.Fatal(err)
	}
	name, data = next()
	if name != "workspaces" || len(data["bindings"].([]any)) != 2 {
		t.Fatal(name, data)
	}
	if err := generic.Simulate("arrival"); err != nil {
		t.Fatal(err)
	}
	name, data = next()
	if name != "change" || data["id"] != second.ID || data["handle"] != second.Handle {
		t.Fatal("generic provider fallback missing", name, data)
	}
	set.Remove(first.ID)
	name, data = next()
	if name != "workspaces" || len(data["bindings"].([]any)) != 1 {
		t.Fatal(name, data)
	}
	a.bump("")
	generic.Simulate("arrival")
	name, data = next()
	if name != "change" || data["id"] != second.ID {
		t.Fatal("removed old handle revived", name, data)
	}
	cancel()
}

func TestWorkspaceMembershipWakeSnapshotAndConcurrentMutation(t *testing.T) {
	set := NewWorkspaceProviders()
	rev, wake, _ := set.snapshot()
	if rev != 0 {
		t.Fatal(rev)
	}
	provider := NewFixture(time.Now)
	_, err := set.Bind(client.Workspace{ID: "default", State: "enrolled"}, provider)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-wake:
	default:
		t.Fatal("successful Bind lost wake")
	}
	rev, wake, entries := set.snapshot()
	if rev != 1 || len(entries) != 1 {
		t.Fatal(rev, entries)
	}
	if _, err = set.Bind(client.Workspace{ID: "default", State: "enrolled"}, provider); err == nil {
		t.Fatal("duplicate bound")
	}
	set.Remove("missing")
	select {
	case <-wake:
		t.Fatal("failed/no-op mutation woke")
	default:
	}
	set.Remove("default")
	select {
	case <-wake:
	default:
		t.Fatal("real Remove lost wake")
	}
	if rev, _, entries = set.snapshot(); rev != 2 || len(entries) != 0 {
		t.Fatal(rev, entries)
	}
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			id := fmt.Sprintf("%032x", worker+1)
			for step := 0; step < 30; step++ {
				set.Bind(client.Workspace{ID: id, State: "enrolled"}, provider)
				set.Remove(id)
			}
		}(worker)
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	for {
		before, ch, _ := set.snapshot()
		after, _, _ := set.snapshot()
		if after > before {
			select {
			case <-ch:
			default:
				t.Fatal("advanced map revision kept old wake open")
			}
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func TestWorkspaceEventsCancelAndRestartDrainOwnedHandler(t *testing.T) {
	for _, action := range []string{"cancel", "restart"} {
		t.Run(action, func(t *testing.T) {
			fixture := NewFixture(time.Now)
			set := NewWorkspaceProviders()
			set.Bind(client.Workspace{ID: "default", State: "enrolled"}, fixture)
			var handler http.Handler
			done := make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(done); handler.ServeHTTP(w, r) }))
			defer ts.Close()
			server := New(fixture, strings.TrimPrefix(ts.URL, "http://"), testToken)
			handler = server.WorkspaceHandler(set)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/workspaces/events", nil)
			req.Header.Set("Cookie", "agentnet_ui="+testToken)
			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			scanner := bufio.NewScanner(res.Body)
			if !scanner.Scan() || scanner.Text() != "event: workspaces" {
				t.Fatal("initial membership absent")
			}
			if action == "cancel" {
				cancel()
			} else {
				server.Restarting()
			}
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("owned stream handler did not drain")
			}
		})
	}
}

type workspaceBrokenWriter struct {
	*httptest.ResponseRecorder
	failFlush bool
}

func (w workspaceBrokenWriter) Write(data []byte) (int, error) {
	if !w.failFlush {
		return 0, fmt.Errorf("closed reader")
	}
	return w.ResponseRecorder.Write(data)
}
func (w workspaceBrokenWriter) FlushError() error { return fmt.Errorf("closed reader") }
func TestWorkspaceEventsWriteOrFlushFailureReturns(t *testing.T) {
	for _, flush := range []bool{false, true} {
		fixture := NewFixture(time.Now)
		set := NewWorkspaceProviders()
		set.Bind(client.Workspace{ID: "default", State: "enrolled"}, fixture)
		server := New(fixture, "127.0.0.1:12345", testToken)
		req := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/workspaces/events", nil)
		req.Header.Set("Cookie", "agentnet_ui="+testToken)
		done := make(chan struct{})
		go func() {
			defer close(done)
			server.WorkspaceHandler(set).ServeHTTP(workspaceBrokenWriter{httptest.NewRecorder(), flush}, req)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("failed local event writer retained handler")
		}
	}
}

// A non-draining authenticated reader may block either Write or Flush. Its
// pending event must never borrow the provider-map lock while doing that I/O.
type workspaceStalledWriter struct {
	*httptest.ResponseRecorder
	flush                  bool
	calls, sent            int
	blocked, release       chan struct{}
	firstSent, changedSent chan struct{}
}

func (w *workspaceStalledWriter) stall() {
	w.calls++
	if w.calls == 2 {
		close(w.blocked)
		<-w.release
	}
}
func (w *workspaceStalledWriter) Write(data []byte) (int, error) {
	if !w.flush {
		w.stall()
	}
	return w.ResponseRecorder.Write(data)
}
func (w *workspaceStalledWriter) FlushError() error {
	if w.flush {
		w.stall()
	}
	w.ResponseRecorder.Flush()
	w.sent++
	if w.sent == 1 {
		close(w.firstSent)
	}
	if w.sent == 3 {
		close(w.changedSent)
	}
	return nil
}

func TestWorkspaceEventsStalledReaderDoesNotLockMembership(t *testing.T) {
	for _, emission := range []string{"content", "membership"} {
		for _, flush := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/flush=%t", emission, flush), func(t *testing.T) {
				provider := &workspacePushProvider{Provider: NewFixture(time.Now), wake: make(chan struct{}), observed: make(chan uint64, 20)}
				set := NewWorkspaceProviders()
				old, err := set.Bind(client.Workspace{ID: "default", State: "enrolled"}, provider)
				if err != nil {
					t.Fatal(err)
				}
				server := New(provider, "127.0.0.1:12345", testToken)
				handler := server.WorkspaceHandler(set)
				ctx, cancel := context.WithCancel(t.Context())
				req := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/workspaces/events", nil).WithContext(ctx)
				req.Header.Set("Cookie", "agentnet_ui="+testToken)
				writer := &workspaceStalledWriter{ResponseRecorder: httptest.NewRecorder(), flush: flush, blocked: make(chan struct{}), release: make(chan struct{}), firstSent: make(chan struct{}), changedSent: make(chan struct{})}
				done := make(chan struct{})
				var operations sync.WaitGroup
				var release sync.Once
				t.Cleanup(func() {
					cancel()
					release.Do(func() { close(writer.release) })
					select {
					case <-done:
					case <-time.After(time.Second):
						t.Error("owned stalled handler did not drain")
					}
					joined := make(chan struct{})
					go func() { operations.Wait(); close(joined) }()
					select {
					case <-joined:
					case <-time.After(time.Second):
						t.Error("owned membership operations did not drain")
					}
				})
				go func() { defer close(done); handler.ServeHTTP(writer, req) }()
				select {
				case <-writer.firstSent:
				case <-time.After(time.Second):
					t.Fatal("initial membership not flushed")
				}
				if emission == "content" {
					provider.bump("")
				} else if _, err := set.Bind(client.Workspace{ID: strings.Repeat("b", 32), State: "enrolled"}, NewFixture(time.Now)); err != nil {
					t.Fatal(err)
				}
				select {
				case <-writer.blocked:
				case <-time.After(time.Second):
					t.Fatal("pending event never reached stalled I/O")
				}
				results := make(chan string, 3)
				bindErrors := make(chan error, 1)
				operations.Add(3)
				go func() {
					defer operations.Done()
					_, err := set.Bind(client.Workspace{ID: strings.Repeat("c", 32), State: "enrolled"}, NewFixture(time.Now))
					bindErrors <- err
					results <- "Bind"
				}()
				go func() { defer operations.Done(); set.Remove(old.ID); results <- "Remove" }()
				go func() { defer operations.Done(); set.List(); results <- "List" }()
				deadline := time.NewTimer(250 * time.Millisecond)
				defer deadline.Stop()
				finished := []string{}
				for len(finished) < 3 {
					select {
					case name := <-results:
						finished = append(finished, name)
					case <-deadline.C:
						t.Fatalf("stalled %s delayed Bind/Remove/List; completed only %v", emission, finished)
					}
				}
				if err := <-bindErrors; err != nil {
					t.Fatal(err)
				}
				stale := httptest.NewRequest("GET", "http://127.0.0.1:12345/workspaces/"+old.ID+"/"+old.Handle+"/api/overview", nil)
				stale.Header.Set("Cookie", "agentnet_ui="+testToken)
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, stale)
				if response.Code != http.StatusConflict {
					t.Fatalf("removed binding authoritative: %d", response.Code)
				}
				rev, _, _ := set.snapshot()
				release.Do(func() { close(writer.release) })
				select {
				case <-writer.changedSent:
				case <-time.After(time.Second):
					t.Fatal("closed membership wake lost after stalled event")
				}
				cancel()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("handler did not stop after cancellation")
				}
				var latest struct {
					Rev      uint64                  `json:"rev"`
					Bindings []workspaceInvalidation `json:"bindings"`
				}
				event := ""
				for _, line := range strings.Split(writer.Body.String(), "\n") {
					if strings.HasPrefix(line, "event: ") {
						event = strings.TrimPrefix(line, "event: ")
					}
					if event == "workspaces" && strings.HasPrefix(line, "data: ") {
						if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &latest); err != nil {
							t.Fatal(err)
						}
					}
				}
				if latest.Rev != rev {
					t.Fatalf("membership revision %d, want %d", latest.Rev, rev)
				}
				found := false
				for _, binding := range latest.Bindings {
					if binding.ID == old.ID || binding.Handle == old.Handle {
						t.Fatal("removed handle returned in authoritative membership")
					}
					found = found || binding.ID == strings.Repeat("c", 32)
				}
				if !found {
					t.Fatal("new binding missing from current membership")
				}
			})
		}
	}
}

func TestHostNoteOfflineAndBehind(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for Comic host note model checks")
	}
	out, err := exec.CommandContext(t.Context(), node, "testdata/host_note_check.mjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}

// The page's directory carries the relay's suspension with presence, and
// neither from a view that is not current.
func TestDirectorySuspendedDevice(t *testing.T) {
	members := protocol.Members{Members: []protocol.Member{{Address: "admin/linux-laptop", Presence: protocol.PresenceOffline, Version: "v0.10.0", Suspended: true}}}
	d := directoryOf(client.MemberView{Listed: client.MembersListed, Current: true, Members: members}, "admin/zenbook")
	if len(d.Members) != 1 || !d.Members[0].Suspended || d.Members[0].Presence != protocol.PresenceOffline {
		t.Fatalf("current view: %+v", d.Members)
	}
	d = directoryOf(client.MemberView{Listed: client.MembersListed, Members: members}, "admin/zenbook")
	if len(d.Members) != 1 || d.Members[0].Suspended || d.Members[0].Presence != "" {
		t.Fatalf("stale view claimed state: %+v", d.Members)
	}
}
