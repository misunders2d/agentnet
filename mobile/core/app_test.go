package core

import (
	"context"
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/client"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/ui"
)

func appClient(t *testing.T, a *App) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	r, e := c.Get(a.URL())
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	return c
}
func TestAppOfflineSecurityAndLifecycle(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	a, e := OpenApp(home, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	c := appClient(t, a)
	for _, path := range []string{"/api/overview", "/api/workspaces", "/assets/skins/index.json"} {
		r, e := c.Get(a.Origin() + path)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != 200 {
			t.Fatalf("%s status %d", path, r.StatusCode)
		}
	}
	r, e := http.Get(a.Origin() + "/api/overview")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 401 {
		t.Fatalf("unauth status %d", r.StatusCode)
	}
	req, _ := http.NewRequest("GET", a.Origin()+"/api/overview", nil)
	req.Host = "attacker.example"
	r, e = c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 421 {
		t.Fatal(r.StatusCode)
	}
	for _, origin := range []string{"", "https://attacker.example", a.Origin()} {
		req, _ := http.NewRequest("POST", a.Origin()+"/api/responder", strings.NewReader(`{"harness":"codex","dir":"/tmp"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		r, e = c.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if origin != a.Origin() && r.StatusCode != 403 {
			t.Fatal(r.StatusCode)
		}
		if origin == a.Origin() && r.StatusCode < 400 {
			t.Fatal("configured phone responder")
		}
	}
	a.session.run = func(ctx context.Context) error { <-ctx.Done(); return nil }
	if e = a.Start(); e != nil {
		t.Fatal(e)
	}
	a.Stop()
	r, e = c.Get(a.Origin() + "/api/overview")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	port := a.Port()
	token := a.URL()
	a.Close()
	b, e := OpenApp(home, port)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	if b.Origin() != a.Origin() || b.URL() == token {
		t.Fatal("unstable origin or reused secret")
	}
}
func TestAppSetupAndOccupiedPort(t *testing.T) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	if a, e := OpenApp(t.TempDir(), l.Addr().(*net.TCPAddr).Port); e == nil {
		a.Close()
		t.Fatal("fell back from occupied port")
	}
	a, e := OpenApp(t.TempDir(), 0)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	c := appClient(t, a)
	r, e := c.Get(a.Origin() + "/api/setup")
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if v := a.setup.SetupInspect("bad"); v.Problem == "" {
		t.Fatal("invalid link accepted")
	}
	if _, e := a.setup.SetupJoin(ui.SetupJoin{Code: "bad"}); e == nil {
		t.Fatal("invalid join accepted")
	}
}
func TestPhoneProviderRejectsLocalExecution(t *testing.T) {
	s, _ := fixture(t)
	p := &phoneProvider{Live: s.live}
	for _, do := range []string{ui.DoAccept, ui.DoAcceptAlways, ui.DoApprove, ui.DoGrantTasks, ui.DoContinue, ui.DoCancel} {
		if _, e := p.Act(ui.Action{Do: do}); e == nil {
			t.Fatalf("allowed %s", do)
		}
	}
	if _, e := p.Folders("/"); e == nil {
		t.Fatal("listed phone filesystem")
	}
	if _, e := p.Send(ui.Draft{ReplyReceiver: &ui.ReplyReceiverSelection{Kind: "managed"}}); e == nil {
		t.Fatal("local receiver allowed")
	}
}

func TestAppWorkspaceIsolationAndReconnect(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	other, otherHome := fixture(t)
	other.Close()
	id := strings.Repeat("a", 32)
	secondHome := filepath.Join(home, "workspaces", id)
	if e := os.MkdirAll(secondHome, 0700); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"identity.json", "agent.db"} {
		data, e := os.ReadFile(filepath.Join(otherHome, name))
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(secondHome, name), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
	registry, e := client.OpenWorkspaces(home)
	if e != nil {
		t.Fatal(e)
	}
	entries, e := registry.List()
	if e != nil {
		t.Fatal(e)
	}
	entries = append(entries, client.Workspace{ID: id, Name: "Second", Endpoint: "https://127.0.0.1:1", Address: "member/phone", State: "enrolled"})
	data, _ := json.Marshal(struct {
		Version int                `json:"version"`
		Items   []client.Workspace `json:"items"`
	}{1, entries})
	if e = os.WriteFile(filepath.Join(home, "workspaces.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	a, e := OpenApp(home, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	c := appClient(t, a)
	bindings := a.providers.List()
	if len(bindings) != 2 {
		t.Fatalf("bindings %v", bindings)
	}
	var handle string
	for _, b := range bindings {
		if b.ID == id {
			handle = b.Handle
		}
	}
	path := "/workspaces/" + id + "/" + handle + "/api/overview"
	r, e := c.Get(a.Origin() + path)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	if e = a.providers.Disconnect(id); e != nil {
		t.Fatal(e)
	}
	r, e = c.Get(a.Origin() + path)
	if e != nil {
		t.Fatal(e)
	}
	r.Body.Close()
	if r.StatusCode != 409 {
		t.Fatal("old handle survives disconnect", r.StatusCode)
	}
	if e = a.providers.Reconnect(id); e != nil {
		t.Fatal(e)
	}
	for _, b := range a.providers.List() {
		if b.ID == id && b.Handle == handle {
			t.Fatal("reused stale handle")
		}
	}
}

func TestPhoneNotificationAvailability(t *testing.T) {
	s, _ := fixture(t)
	registered := false
	p := &phoneProvider{Live: s.live, available: func() bool { return registered }}
	for _, overview := range []func() (ui.Overview, error){p.Overview, p.TopicOverview} {
		v, e := overview()
		if e != nil {
			t.Fatal(e)
		}
		if v.Notify == nil || v.Notify.Native || v.Notify.Available {
			t.Fatal("claimed unavailable native notifications")
		}
	}
	if _, e := p.NotifyEnable(); e == nil {
		t.Fatal("enabled without native callback")
	}
	registered = true
	v, e := p.TopicOverview()
	if e != nil || !v.Notify.Native || !v.Notify.Available {
		t.Fatal("registered callback unavailable", e)
	}
}

func TestDisconnectedDefaultAuthenticationPrecedence(t *testing.T) {
	s, home := fixture(t)
	s.Close()
	a, e := OpenApp(home, 0)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	c := appClient(t, a)
	if e = a.providers.Disconnect(client.DefaultWorkspace); e != nil {
		t.Fatal(e)
	}
	cases := []struct {
		client               *http.Client
		host, origin, method string
		want                 int
	}{{http.DefaultClient, "", "", "GET", 401}, {c, "attacker.invalid", "", "GET", 421}, {c, "", "https://attacker.invalid", "POST", 403}, {c, "", a.Origin(), "POST", 409}, {c, "", "", "GET", 409}}
	for _, tc := range cases {
		req, _ := http.NewRequest(tc.method, a.Origin()+"/api/overview", strings.NewReader(`{}`))
		if tc.host != "" {
			req.Host = tc.host
		}
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Content-Type", "application/json")
		r, e := tc.client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		r.Body.Close()
		if r.StatusCode != tc.want {
			t.Fatalf("%s host=%s origin=%s: %d want%d", tc.method, tc.host, tc.origin, r.StatusCode, tc.want)
		}
	}
}
