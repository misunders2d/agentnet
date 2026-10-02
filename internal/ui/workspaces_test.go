package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
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
