package ui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
	"github.com/misunders2d/agentnet/internal/ui/static"
)

type fakeSetup struct {
	joined []SetupJoin
	starts int
}

func (f *fakeSetup) SetupState() SetupView {
	return SetupView{State: "none", Device: "linux-laptop", DeviceWords: "Linux laptop"}
}
func (f *fakeSetup) SetupInspect(code string) SetupInvite {
	return SetupInvite{Kind: "invite", Host: "hub.example.test", From: "Sergey"}
}
func (f *fakeSetup) SetupJoin(j SetupJoin) (SetupResult, error) {
	f.joined = append(f.joined, j)
	return SetupResult{Host: "hub.example.test"}, nil
}
func (f *fakeSetup) SetupStartAgain() (SetupView, error) {
	f.starts++
	return f.SetupState(), nil
}

// The app's first-run page has the messenger page's guard: its Host, the
// token traded once for the cookie, and same-origin JSON for changes. It
// serves its own script and nothing of the messenger.
func TestSetupPage(t *testing.T) {
	p := &fakeSetup{}
	var h http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	defer ts.Close()
	h = NewSetup(p, strings.TrimPrefix(ts.URL, "http://"), testToken)
	if r := do(t, ts, "GET", "/", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	if r := do(t, ts, "GET", "/?t="+testToken, "", nil); r.StatusCode != http.StatusSeeOther || !strings.Contains(r.Header.Get("Set-Cookie"), testToken) {
		t.Fatalf("token: %d", r.StatusCode)
	}
	r := do(t, ts, "GET", "/", "", authed(ts, nil))
	page, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !strings.Contains(string(page), `src="/assets/setup.mjs"`) || strings.Contains(string(page), "loader.js") || strings.Contains(string(page), "manifest") {
		t.Fatalf("setup page: %d %s", r.StatusCode, page)
	}
	if r := do(t, ts, "GET", "/assets/setup.mjs", "", authed(ts, nil)); r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("setup.mjs: %d", r.StatusCode)
	}
	for _, other := range []string{"/assets/loader.js", "/assets/engine.mjs", "/api/overview", "/manifest.webmanifest"} {
		if r := do(t, ts, "GET", other, "", authed(ts, nil)); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s on the setup page: %d", other, r.StatusCode)
		}
	}
	body := `{"code":"agentnet-invite-v1:x","name":"Bohdan"}`
	if r := do(t, ts, "POST", "/api/setup/join", body, authed(ts, map[string]string{"Content-Type": "application/json"})); r.StatusCode != http.StatusForbidden || len(p.joined) != 0 {
		t.Fatalf("cross-origin join: %d", r.StatusCode)
	}
	if r := do(t, ts, "POST", "/api/setup/join", body, post(ts)); r.StatusCode != 200 || len(p.joined) != 1 || p.joined[0].Name != "Bohdan" {
		t.Fatalf("join: %d %+v", r.StatusCode, p.joined)
	}
	if r := do(t, ts, "POST", "/api/setup/start-again", `{}`, authed(ts, map[string]string{"Content-Type": "application/json"})); r.StatusCode != http.StatusForbidden || p.starts != 0 {
		t.Fatalf("cross-origin start again: %d", r.StatusCode)
	}
	if r := do(t, ts, "POST", "/api/setup/start-again", `{}`, post(ts)); r.StatusCode != 200 || p.starts != 1 {
		t.Fatalf("start again: %d", r.StatusCode)
	}
	r = do(t, ts, "GET", "/api/setup", "", authed(ts, nil))
	var v SetupView
	json.NewDecoder(r.Body).Decode(&v)
	if v.State != "none" || v.DeviceWords != "Linux laptop" {
		t.Fatalf("state %+v", v)
	}
}

// The daemon's page is no installable web app: the AgentNet app is the
// one way to open it (MEL-536), so no manifest is served or linked.
func TestNativeManifestNotServed(t *testing.T) {
	ts, _ := newTestServer(t)
	if r := do(t, ts, "GET", "/manifest.webmanifest", "", authed(ts, nil)); r.StatusCode != http.StatusNotFound {
		t.Fatalf("manifest: %d", r.StatusCode)
	}
	if r := do(t, ts, "GET", "/assets/manifest.webmanifest", "", authed(ts, nil)); r.StatusCode != http.StatusNotFound {
		t.Fatalf("manifest asset: %d", r.StatusCode)
	}
	r := do(t, ts, "GET", "/", "", authed(ts, nil))
	page, _ := io.ReadAll(r.Body)
	if strings.Contains(string(page), "manifest") {
		t.Fatal("the page links a manifest")
	}
	if r := do(t, ts, "GET", "/api/overview", "", nil); r.StatusCode != http.StatusUnauthorized {
		t.Fatal("no session")
	} else if b, _ := io.ReadAll(r.Body); !strings.Contains(string(b), "AgentNet app") || strings.Contains(string(b), "run agentnet ui") {
		t.Fatalf("401 text %q", b)
	}
}

func getJSON(t *testing.T, ts *httptest.Server, path string, out any) int {
	t.Helper()
	r := do(t, ts, "GET", path, "", authed(ts, nil))
	json.NewDecoder(r.Body).Decode(out)
	return r.StatusCode
}

func postJSON(t *testing.T, ts *httptest.Server, path, body string, out any) int {
	t.Helper()
	r := do(t, ts, "POST", path, body, post(ts))
	if out != nil {
		json.NewDecoder(r.Body).Decode(out)
	}
	return r.StatusCode
}

// The host API's app routes, as the demo serves them for the skins:
// where to get the app, invitations, the folder picker and the device
// link's app link.
func TestGetAppAndInvitesRoutes(t *testing.T) {
	ts, _ := newTestServer(t)
	var app GetAppView
	if code := getJSON(t, ts, "/api/get-app", &app); code != 200 || len(app.Platforms) != len(static.Downloads("dev")) || app.Version == "" {
		t.Fatalf("get-app: %d %+v", code, app)
	}
	for _, p := range app.Platforms {
		if !strings.HasPrefix(p.URL, static.Releases()+"/") || p.Label == "" {
			t.Fatalf("download %+v", p)
		}
	}
	var list InvitesView
	if code := getJSON(t, ts, "/api/invites", &list); code != 200 || !list.CanInvite || len(list.Invites) != 1 {
		t.Fatalf("invites: %d %+v", code, list)
	}
	var made InviteView
	if code := postJSON(t, ts, "/api/invite", `{"name":"Bohdan K","admin":false,"days":7}`, &made); code != 200 || made.Link == "" || !strings.Contains(made.Message, made.Link) {
		t.Fatalf("invite: %d %+v", code, made)
	}
	if code := postJSON(t, ts, "/api/invite", `{"name":"Bohdan","days":3}`, nil); code != http.StatusConflict {
		t.Fatalf("3 days: %d", code)
	}
	getJSON(t, ts, "/api/invites", &list)
	if len(list.Invites) != 2 || list.Invites[0].Name != "Bohdan K" {
		t.Fatalf("after invite: %+v", list)
	}
	if code := postJSON(t, ts, "/api/invite/revoke", `{"id":"`+list.Invites[0].ID+`"}`, nil); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if code := postJSON(t, ts, "/api/invite/revoke", `{"id":"`+list.Invites[0].ID+`"}`, nil); code != http.StatusNotFound {
		t.Fatalf("revoke twice: %d", code)
	}
	var dirs FoldersView
	if code := getJSON(t, ts, "/api/folders", &dirs); code != 200 || dirs.Path != dirs.Home || len(dirs.Dirs) == 0 {
		t.Fatalf("folders: %d %+v", code, dirs)
	}
}

func TestInviteMessage(t *testing.T) {
	if got := InviteMessage("Sergey", "https://x/#c"); got != "Sergey invited you to AgentNet. Open this link to get the app and join: https://x/#c" {
		t.Fatal(got)
	}
	if got := InviteMessage("", "L"); !strings.HasPrefix(got, "You're invited to AgentNet.") {
		t.Fatal(got)
	}
}

// The folder picker lists folders only, not hidden ones, follows links to
// folders, sorts them, and knows the parent and the home; it refuses a
// relative path and says when a folder cannot be opened.
func TestFolders(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"beta", "Alpha", ".hidden", "gamma/inner"} {
		os.MkdirAll(filepath.Join(root, d), 0o755)
	}
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644)
	if runtime.GOOS != "windows" {
		os.Symlink(filepath.Join(root, "gamma"), filepath.Join(root, "link-to-gamma"))
	}
	v, err := listFolders(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range v.Dirs {
		names = append(names, d.Name)
		if d.Path != filepath.Join(root, d.Name) {
			t.Fatalf("path %q", d.Path)
		}
	}
	want := "Alpha beta gamma link-to-gamma"
	if runtime.GOOS == "windows" {
		want = "Alpha beta gamma"
	}
	if strings.Join(names, " ") != want || v.Parent != filepath.Dir(root) || v.Path != root {
		t.Fatalf("listing %v parent %q", names, v.Parent)
	}
	if _, err := listFolders("relative/path"); err == nil {
		t.Fatal("a relative path listed")
	}
	if _, err := listFolders(filepath.Join(root, "missing")); err == nil {
		t.Fatal("a missing folder listed")
	}
	if home, _ := os.UserHomeDir(); home != "" {
		if v, err := listFolders(""); err != nil || v.Path != filepath.Clean(home) {
			t.Fatalf("home listing %q %v", v.Path, err)
		}
	}
}

// Invitations through the daemon's page: an admin's device may invite and
// sees the waiting invitations; a Hub whose page browsers cannot trust
// (this test Hub pins its certificate) refuses a link in plain words.
func TestLiveInvites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, filepath.Join(t.TempDir(), "admin"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	l := NewLive(a)
	if _, err := l.Invite(InviteRequest{Name: "Bohdan", Days: 7}); err == nil || !strings.Contains(err.Error(), "browsers trust") {
		t.Fatalf("a link from a pinned Hub: %v", err)
	}
	if _, err := l.Invite(InviteRequest{Name: "", Days: 7}); err == nil {
		t.Fatal("no name")
	}
	if _, err := a.Invite(ctx, "dana", time.Hour, false); err != nil {
		t.Fatal(err)
	}
	v, err := l.Invites()
	if err != nil || !v.CanInvite || len(v.Invites) != 1 || v.Invites[0].Label != "dana" {
		t.Fatalf("invites %+v %v", v, err)
	}
	if err := l.RevokeInvite(v.Invites[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := l.RevokeInvite(v.Invites[0].ID); err == nil {
		t.Fatal("withdrawn twice")
	}
}
