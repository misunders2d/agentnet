package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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
