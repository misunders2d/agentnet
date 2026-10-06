package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/hub"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/testhub"
)

// The browser engine serves the app routes as the daemon's page does:
// an admin's browser sees whether it may invite and the waiting
// invitations and withdraws one; another member may not; a server whose
// page browsers cannot trust refuses a link in plain words; the app's
// downloads come from getapp.json; folders are refused; a device link
// carries its app link. Two browsers given the same label and device name
// get the next free name, never a question.
func TestBrowserEngineInvites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.StartConfig(t, hub.Config{DataDir: dir, Web: true}, "127.0.0.1:0")
	laptop, err := client.Join(ctx, filepath.Join(t.TempDir(), "laptop"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { laptop.Close() })
	adminCode, _ := laptop.Invite(ctx, "eve", time.Hour, true)
	memberCode, _ := laptop.Invite(ctx, "eve", time.Hour, false)
	if _, err := laptop.Invite(ctx, "dana", time.Hour, false); err != nil {
		t.Fatal(err)
	}
	base := "https://" + h.Addr
	admin := startEngineNode(t, dir)
	admin.ok(map[string]any{"op": "init", "base": base})
	if a := admin.ok(map[string]any{"op": "joinAuto", "code": browserCode(t, adminCode), "base": "android-phone"})["address"]; a != "eve/android-phone" {
		t.Fatalf("first browser: %v", a)
	}
	member := startEngineNode(t, dir)
	member.ok(map[string]any{"op": "init", "base": base})
	if a := member.ok(map[string]any{"op": "joinAuto", "code": browserCode(t, memberCode), "base": "android-phone"})["address"]; a != "eve/android-phone-2" {
		t.Fatalf("a taken name was not followed by the next one: %v", a)
	}
	list := admin.api("/api/invites", nil)
	invites, _ := list["invites"].([]any)
	if list["can_invite"] != true || len(invites) != 1 || invites[0].(map[string]any)["label"] != "dana" {
		t.Fatalf("admin's list: %v", list)
	}
	if m := member.api("/api/invites", nil); m["can_invite"] != false || len(m["invites"].([]any)) != 0 {
		t.Fatalf("member's list: %v", m)
	}
	id := invites[0].(map[string]any)["id"].(string)
	member.refuses("a member withdraws", member.call(map[string]any{"op": "api", "path": "/api/invite/revoke", "body": map[string]any{"id": id}}), "Only an admin")
	admin.refuses("no name", admin.call(map[string]any{"op": "api", "path": "/api/invite", "body": map[string]any{"name": " ", "days": 7}}), "Write the name")
	admin.refuses("3 days", admin.call(map[string]any{"op": "api", "path": "/api/invite", "body": map[string]any{"name": "Bob", "days": 3}}), "1, 7 or 30 days")
	admin.refuses("a link from a pinned server", admin.call(map[string]any{"op": "api", "path": "/api/invite", "body": map[string]any{"name": "Bob", "days": 7}}), "browsers trust")
	admin.ok(map[string]any{"op": "api", "path": "/api/invite/revoke", "body": map[string]any{"id": id}})
	admin.refuses("withdrawn twice", admin.call(map[string]any{"op": "api", "path": "/api/invite/revoke", "body": map[string]any{"id": id}}), "already used")
	app := admin.api("/api/get-app", nil)
	if ps, _ := app["platforms"].([]any); len(ps) < 5 || !strings.Contains(ps[0].(map[string]any)["url"].(string), "/releases/") {
		t.Fatalf("get-app: %v", app)
	}
	admin.refuses("folders in a browser", admin.call(map[string]any{"op": "api", "path": "/api/folders"}), "AgentNet app")
	admin.ok(map[string]any{"op": "person", "label": "Eve"})
	admin.ok(map[string]any{"op": "start"})
	var link map[string]any
	admin.until("a device link", func() bool {
		v := admin.call(map[string]any{"op": "api", "path": "/api/device/link", "body": map[string]any{}})
		link, _ = v["v"].(map[string]any)
		return link != nil
	})
	if u, _ := link["url"].(string); link["app_url"] != "agentnet://open#"+u[strings.Index(u, "#")+1:] {
		t.Fatalf("device link %v", link)
	}
}

// The browser reads an invitation's hints exactly as Go does, dropping
// what is not readable text.
func TestInviteDecodeParity(t *testing.T) {
	w := startEngineNode(t, t.TempDir())
	for _, inv := range []protocol.Invite{
		{Hub: "https://hub.example.test", Label: "bohdan", Secret: "s", Name: "Bohdan K", From: "Sergey", Workspace: "Mellanni"},
		{Hub: "https://hub.example.test", Label: "b", Secret: "s", Name: "Eve‮", From: " Sergey", Workspace: strings.Repeat("w", protocol.MaxWorkspaceHint+1)},
		{Hub: "https://hub.example.test", Label: "b", Secret: "s", Name: "Анна-Марія", From: "O'Neil & Co."},
		{Hub: "https://hub.example.test", Label: "b", Secret: "s"},
	} {
		code := inv.Encode()
		goInv, err := protocol.DecodeInvite(code)
		if err != nil {
			t.Fatal(err)
		}
		js := w.ok(map[string]any{"op": "decodeInvite", "code": code})["v"].(map[string]any)
		if js["name"] != goInv.Name || js["from"] != goInv.From || js["workspace"] != goInv.Workspace || js["label"] != goInv.Label {
			t.Errorf("%+v: browser %v, Go %+v", inv, js, goInv)
		}
	}
}

// platformHub is a relay as a platform runs it: the Hub serves plain HTTP
// behind HTTPS it does not hold (PlatformTLS, web on), so its invitations
// carry no certificate pin and invitation links can be made. The front's
// certificate is written to dir/tls.crt, where engine nodes take their
// trust from; the Go admin pins it (its bootstrap invitation is given it).
func platformHub(t *testing.T, ctx context.Context) (dir, base string, admin *client.Agent) {
	t.Helper()
	var proxy atomic.Pointer[httputil.ReverseProxy]
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxy.Load().ServeHTTP(w, r) }))
	front.StartTLS()
	t.Cleanup(front.Close)
	base = front.URL
	dir = filepath.Join(t.TempDir(), "hub")
	h := testhub.StartConfig(t, hub.Config{DataDir: dir, PublicURL: base, PlatformTLS: true, Web: true}, "127.0.0.1:0")
	proxy.Store(httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: h.Addr}))
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: front.Certificate().Raw}))
	if err := os.WriteFile(filepath.Join(dir, "tls.crt"), []byte(certPEM), 0o644); err != nil {
		t.Fatal(err)
	}
	inv, err := protocol.DecodeInvite(testhub.BootstrapCode(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	inv.CertPEM = certPEM
	admin, err = client.Join(ctx, filepath.Join(t.TempDir(), "laptop"), inv.Encode(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	return dir, base, admin
}

// Invite people succeeds the same way from the app and from a browser on
// a relay browsers trust: a link to the relay's page with the invitation
// in its fragment, the label the relay made from the name, the expiry
// chosen, and the message ready to send. The waiting invitations read the
// same in both, an invitation made before the relay kept its date
// included (no date, not year 1).
func TestInviteLinkGoAndBrowser(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir, base, laptop := platformHub(t, ctx)
	check := func(who string, v InviteView, name, label string) {
		t.Helper()
		code, ok := strings.CutPrefix(v.Link, base+"/#")
		inv, err := protocol.DecodeInvite(code)
		if !ok || err != nil || inv.Name != name || inv.Label != label || inv.CertPEM != "" || inv.Hub != base {
			t.Fatalf("%s: link %q (%+v %v)", who, v.Link, inv, err)
		}
		if v.Label != label || v.Message != InviteMessage("", v.Link) {
			t.Fatalf("%s: %+v", who, v)
		}
		if d := time.Until(v.Expires) - 7*24*time.Hour; d > time.Minute || d < -time.Minute {
			t.Fatalf("%s: expires %s", who, v.Expires)
		}
	}
	made, err := NewLive(laptop).Invite(InviteRequest{Name: "Bohdan K", Days: 7})
	if err != nil {
		t.Fatal(err)
	}
	check("Go", made, "Bohdan K", "bohdan-k")
	browserCode, err := laptop.Invite(ctx, "eve", time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	w.ok(map[string]any{"op": "join", "code": browserCode, "name": "phone"})
	raw, _ := json.Marshal(w.api("/api/invite", map[string]any{"name": "Olena", "days": 7}))
	var js InviteView
	if err := json.Unmarshal(raw, &js); err != nil {
		t.Fatal(err)
	}
	check("browser", js, "Olena", "olena")

	// An invitation from before the relay kept invitation dates.
	if _, err := laptop.Invite(ctx, "dana", time.Hour, false); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`UPDATE invites SET created_at = NULL WHERE label = 'dana'`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	goList, err := NewLive(laptop).Invites()
	if err != nil {
		t.Fatal(err)
	}
	goRaw, _ := json.Marshal(goList)
	var goView, jsView struct {
		Invites []map[string]any `json:"invites"`
	}
	json.Unmarshal(goRaw, &goView)
	jsRaw, _ := json.Marshal(w.api("/api/invites", nil))
	json.Unmarshal(jsRaw, &jsView)
	byLabel := func(list []map[string]any, label string) map[string]any {
		for _, i := range list {
			if i["label"] == label {
				return i
			}
		}
		return nil
	}
	for _, label := range []string{"bohdan-k", "olena", "dana"} {
		g, b := byLabel(goView.Invites, label), byLabel(jsView.Invites, label)
		if g == nil || b == nil {
			t.Fatalf("%s: Go %v, browser %v", label, goView.Invites, jsView.Invites)
		}
		_, gc := g["created"]
		_, bc := b["created"]
		if gc != bc || gc != (label != "dana") {
			t.Errorf("%s created: Go %v, browser %v", label, g["created"], b["created"])
		}
	}
}

// A device link whose person's devices changed since it was made is
// refused once, in the words the app uses: no other device names tried,
// and never "all names taken".
func TestBrowserEngineStaleLinkNotRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	h := testhub.StartConfig(t, hub.Config{DataDir: dir, Web: true}, "127.0.0.1:0")
	laptop := personAgent(t, ctx, testhub.BootstrapCode(t, dir), "laptop", "Eve")
	var offer client.DeviceLinkOffer
	var err error
	waitFor(t, "a device link", func() bool { offer, err = laptop.NewDeviceLink(ctx); return err == nil })
	if _, err := laptop.RenamePerson(ctx, "Eve K"); err != nil {
		t.Fatal(err)
	}
	o, err := protocol.DecodeLinkOffer(offer.Code)
	if err != nil {
		t.Fatal(err)
	}
	o.Invite = browserCode(t, o.Invite)
	base := "https://" + h.Addr
	w := startEngineNode(t, dir)
	w.ok(map[string]any{"op": "init", "base": base})
	v := w.call(map[string]any{"op": "joinLinkAuto", "code": base + "/#" + o.Encode(), "base": "android-phone"})
	w.refuses("a stale link", v, "Your devices changed since that link was made")
	if e, _ := v["error"].(string); strings.Contains(e, "taken") {
		t.Fatalf("a stale link was retried under other names: %s", e)
	}
}

// The engine's copies of Go's tunables are Go's.
func TestEngineTunablesMatchGo(t *testing.T) {
	w := startEngineNode(t, t.TempDir())
	v := w.ok(map[string]any{"op": "tunables"})
	var days []int
	for _, d := range v["inviteDays"].([]any) {
		days = append(days, int(d.(float64)))
	}
	if !slices.Equal(days, InviteDays) {
		t.Fatalf("invitation days: browser %v, Go %v", days, InviteDays)
	}
}
