package ui

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestLiveAgentCatalogGuardLocalSaveAndVerifiedSelection(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	bin := t.TempDir()
	invoked := filepath.Join(bin, "invoked")
	for _, harness := range []string{"pi", "claude", "codex"} {
		writeUIHarnessStub(t, bin, harness, "#!/bin/sh\ntouch "+invoked+"\n", "type nul > \""+invoked+"\"\r\n")
	}
	t.Setenv("PATH", bin)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	hub := testhub.Start(t, dir, "127.0.0.1:0", "")
	code := testhub.BootstrapCode(t, dir)
	home := filepath.Join(t.TempDir(), "alice")
	a, err := client.Join(ctx, home, code, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	l := NewLive(a)
	s := New(l, "127.0.0.1:8123", testToken)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents", s.agentCatalog)
	mux.HandleFunc("POST /api/agents", s.changeAgent)
	handler := s.guard(mux)
	request := func(method, path, body, origin string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8123"+path, strings.NewReader(body))
		if auth {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		}
		if method == "POST" {
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, path, body, origin string
		auth                       bool
		status                     int
	}{
		{"GET", "/api/agents", "", "", false, 401},
		{"POST", "/api/agents", `{"action":"create"}`, "https://foreign.test", true, 403},
		{"POST", "/api/agents", `{"action":"create","program":"chosen remotely"}`, "http://127.0.0.1:8123", true, 400},
	} {
		if w := request(tc.method, tc.path, tc.body, tc.origin, tc.auth); w.Code != tc.status {
			t.Fatalf("guard %d %s", w.Code, w.Body)
		}
	}
	if entries, _ := a.LocalAgents(); len(entries) != 0 {
		t.Fatal("guard mutated catalog")
	}
	work := t.TempDir()
	contextFile := filepath.Join(work, "local-context")
	if err = os.WriteFile(contextFile, []byte("local trusted context"), 0600); err != nil {
		t.Fatal(err)
	}
	defaultConfig := &client.Responder{Harness: "pi", Dir: work, Timeout: 77 * time.Second, Context: []string{contextFile}}
	if err = a.SetResponder(defaultConfig); err != nil {
		t.Fatal(err)
	}
	before, _ := a.Responder()
	body, _ := json.Marshal(AgentCatalogChange{Action: "create", Label: "Builder", Harness: "claude", Dir: work})
	w := request("POST", "/api/agents", string(body), "http://127.0.0.1:8123", true)
	var created AgentCatalogChangeResult
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &created) != nil || !created.Saved || !created.Published || created.Agent == nil || !created.Agent.Responder.Ready {
		t.Fatalf("create %d %s", w.Code, w.Body)
	}
	id := created.Agent.Record.ID
	if err = created.Agent.Record.Verify(a.Self()); err != nil {
		t.Fatal(err)
	}
	// Preserve existing local timeout/context when selecting new next-job dir.
	namedConfig := *defaultConfig
	namedConfig.Harness = "claude"
	if err = a.SetLocalAgentResponder(id, &namedConfig); err != nil {
		t.Fatal(err)
	}
	nextDir := t.TempDir()
	updated, err := l.ChangeAgent(ctx, AgentCatalogChange{Action: "update", ID: id, Dir: nextDir})
	if err != nil || updated.Agent.Responder.Timeout != 77 || !reflect.DeepEqual(updated.Agent.Responder.Context, defaultConfig.Context) {
		t.Fatalf("update %+v %v", updated, err)
	}
	after, _ := a.Responder()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("catalog changed device default")
	}
	if _, err = l.ChangeAgent(ctx, AgentCatalogChange{Action: "update", ID: id, Harness: "unsupported"}); !errors.Is(err, ErrRefused) {
		t.Fatal("unsupported harness", err)
	}
	if _, err = l.ChangeAgent(ctx, AgentCatalogChange{Action: "update", ID: protocol.NewID(), Dir: work}); !errors.Is(err, ErrRefused) {
		t.Fatal("foreign/unknown local update", err)
	}
	get := request("GET", "/api/agents", "", "", true)
	var local AgentCatalogView
	if get.Code != 200 || json.Unmarshal(get.Body.Bytes(), &local) != nil || !local.Local || len(local.Agents) != 2 || !local.Agents[0].Default || local.Agents[1].Responder.Dir != nextDir {
		t.Fatalf("local view %d %s", get.Code, get.Body)
	}

	// Signed synthetic session capabilities permit verified public discovery.
	runDaemon(t, a)
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(inv.CertPEM))
	key, err := identity.Load(filepath.Join(home, "identity.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw := &rawAgent{t: t, id: key, addr: a.Address, hub: inv.Hub, http: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: time.Second * 5}}
	var profile protocol.Profile
	deadline := time.Now().Add(10 * time.Second)
	for len(profile.Sessions) != 1 {
		status, data := raw.do("GET", "/v1/agents/admin/laptop/profile", nil, true)
		if status != 200 || json.Unmarshal(data, &profile) != nil {
			t.Fatalf("profile %d %s", status, data)
		}
		if time.Now().After(deadline) {
			t.Fatal("session not published")
		}
		if len(profile.Sessions) != 1 {
			time.Sleep(10 * time.Millisecond)
		}
	}
	caps := protocol.CapsRecord{Address: a.Address, Session: profile.Sessions[0], Caps: []string{protocol.CapAgentIdentity}, TS: time.Now().Unix() + 100}
	caps.Sign(key.Sign)
	if status, data := raw.do("PUT", "/v1/caps", marshalBytes(t, caps), true); status != http.StatusNoContent {
		t.Fatalf("caps %d %s", status, data)
	}
	remote, err := l.AgentCatalog(ctx, a.Address)
	if err != nil || remote.Local || len(remote.Agents) != 2 || remote.Agents[0].Responder != nil || len(remote.Harnesses) != 0 {
		t.Fatalf("public isolation %+v %v", remote, err)
	}
	// A chosen agent is reached in a chat now, never by a new device
	// conversation (MEL-713 step 1).
	if _, err = l.Send(Draft{To: a.Address, Kind: KindQuestion, Body: "named", AgentID: id}); !errors.Is(err, ErrDeviceThreadReadOnly) {
		t.Fatalf("named device request %v", err)
	}

	disabled, err := l.ChangeAgent(ctx, AgentCatalogChange{Action: "disable", ID: id})
	if err != nil || disabled.Agent.Enabled || !disabled.Published {
		t.Fatalf("disable %+v %v", disabled, err)
	}
	if _, err = os.Stat(invoked); !os.IsNotExist(err) {
		t.Fatal("configuration executed harness", err)
	}

	hub.Stop()
	l.timeout = 100 * time.Millisecond
	offline, err := l.ChangeAgent(ctx, AgentCatalogChange{Action: "create", Label: "Saved offline", Harness: "codex", Dir: work})
	if err != nil || !offline.Saved || offline.Published || offline.Agent == nil || !strings.Contains(offline.Note, "not confirmed") {
		t.Fatalf("offline save %+v %v", offline, err)
	}
	entries, err := a.LocalAgents()
	if err != nil || len(entries) != 3 {
		t.Fatal("offline rollback", err)
	}
}
