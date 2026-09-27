package hub

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func enrollAdmin(t *testing.T, h *Hub, label string) member {
	t.Helper()
	id, _ := identity.Generate()
	secret := protocol.NewID()
	if err := h.store.createInvite(secret, label, true, time.Hour, "admin/test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.enroll(secret, id.Public(label+"/x"), label); err != nil {
		t.Fatal(err)
	}
	return member{id, label + "/x"}
}

// Only admins set the recommendation, and only valid ones; it survives a
// Hub restart.
func TestReleaseAdminAndValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	h, err := Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	admin, bob := enrollAdmin(t, h, "boss"), enroll(t, h, "bob")
	good := protocol.ReleaseRequest{Release: protocol.Release{Version: "v1.2.3", URL: "https://example.test/update", Note: "please update"}}
	if c, _ := bob.call(t, h, "POST", "/v1/admin/release", good); c != http.StatusForbidden {
		t.Fatalf("non-admin: %d", c)
	}
	for _, bad := range []protocol.Release{
		{Version: "v1", URL: "http://example.test/"},
		{Version: "v1", URL: "https://user:pw@example.test/"},
		{Version: "v1 two", URL: "https://example.test/"},
		{Version: "v1", URL: "https://example.test/\x07"},
		{Version: strings.Repeat("9", 65), URL: "https://example.test/"},
		{Version: "v1", URL: "https://example.test/", Note: "two\nlines"},
	} {
		if c, _ := admin.call(t, h, "POST", "/v1/admin/release", protocol.ReleaseRequest{Release: bad}); c != http.StatusBadRequest {
			t.Fatalf("%+v accepted: %d", bad, c)
		}
	}
	if r, _ := h.currentRelease(); r.Version != "" {
		t.Fatalf("state changed by rejected requests: %+v", r)
	}
	if c, b := admin.call(t, h, "POST", "/v1/admin/release", good); c != http.StatusOK {
		t.Fatalf("set: %d %s", c, b)
	}
	if c, b := bob.call(t, h, "GET", "/v1/release", nil); c != http.StatusOK || !strings.Contains(string(b), "v1.2.3") {
		t.Fatalf("member read: %d %s", c, b)
	}
	h.Close()
	h2, err := Open(Config{DataDir: dir, PublicURL: "https://127.0.0.1:1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	defer h2.Close()
	if r, _ := h2.currentRelease(); r != good.Release {
		t.Fatalf("after restart: %+v", r)
	}
}

// A connected stream gets the recommendation on connect and on every real
// change, including two within one second; setting it again sends nothing.
func TestReleasePushedOnStream(t *testing.T) {
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf, Heartbeat: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	admin, bob := enrollAdmin(t, h, "boss"), enroll(t, h, "bob")
	srv := httptest.NewUnstartedServer(h.routes())
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	ad := protocol.SessionAd{Address: bob.addr, Session: protocol.NewID()}
	protocol.SignAd(&ad, bob.id.Sign)
	req, _ := http.NewRequest("GET", srv.URL+"/v1/stream?ad="+ad.Encode(), nil)
	protocol.SignRequest(req, bob.addr, bob.id.Sign, nil)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	guard := time.AfterFunc(10*time.Second, func() { resp.Body.Close() })
	defer guard.Stop()
	sc := bufio.NewScanner(resp.Body)
	next := func() (event, data string) {
		for sc.Scan() {
			line := sc.Text()
			if e, ok := strings.CutPrefix(line, "event: "); ok {
				event = e
			} else if d, ok := strings.CutPrefix(line, "data: "); ok {
				return event, d
			}
		}
		t.Fatalf("stream ended: %v", sc.Err())
		return
	}
	set := func(r protocol.Release) {
		t.Helper()
		if c, b := admin.call(t, h, "POST", "/v1/admin/release", protocol.ReleaseRequest{Release: r, Clear: r.Version == ""}); c != http.StatusOK {
			t.Fatalf("set %+v: %d %s", r, c, b)
		}
	}
	if e, d := next(); e != "release" || !strings.Contains(d, `"version":""`) {
		t.Fatalf("on connect: %s %s", e, d)
	}
	r1 := protocol.Release{Version: "v2", URL: "https://example.test/a"}
	set(r1)
	if e, d := next(); e != "release" || !strings.Contains(d, `"version":"v2"`) {
		t.Fatalf("after set: %s %s", e, d)
	}
	set(r1) // same again: nothing new
	set(protocol.Release{Version: "v2", URL: "https://example.test/b"})
	if e, d := next(); e != "release" || !strings.Contains(d, "example.test/b") {
		t.Fatalf("same-second change: %s %s", e, d)
	}
	set(protocol.Release{})
	if e, d := next(); e != "release" || !strings.Contains(d, `"version":""`) {
		t.Fatalf("after clear: %s %s", e, d)
	}
}
