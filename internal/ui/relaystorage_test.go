package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/drivecontract"
	"github.com/misunders2d/agentnet/internal/testhub"
)

type relayStorageFixture struct {
	*Fixture
	mu      sync.Mutex
	reads   int
	changes []drivecontract.RelayStorageAction
}

func (f *relayStorageFixture) RelayAttachmentStorage(context.Context) (drivecontract.RelayStorageStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	return drivecontract.RelayStorageStatus{State: "local"}, nil
}
func (f *relayStorageFixture) ChangeRelayAttachmentStorage(_ context.Context, r drivecontract.RelayStorageAction) (drivecontract.RelayStorageStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes = append(f.changes, r)
	return drivecontract.RelayStorageStatus{State: "local", CanAdmin: true}, nil
}

const relayStorageHost = "127.0.0.1:8123"

func relayStorageCall(handler http.Handler, method, body string, header map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+relayStorageHost+"/api/storage/attachments", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://"+relayStorageHost)
	r.Header.Set("Cookie", cookieName+"="+testToken)
	for k, v := range header {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

// The page's guard stands before any relay change: unauthenticated,
// cross-origin, cross-site, form and malformed requests never reach the
// provider; reading never changes; a provider without relay transport is 404.
func TestRelayStorageRouteGuardAndOptionalProvider(t *testing.T) {
	f := &relayStorageFixture{Fixture: NewFixture(time.Now)}
	h := New(f, relayStorageHost, testToken).Handler()
	for _, tc := range []struct {
		method, body string
		header       map[string]string
		status       int
	}{
		{"POST", `{"action":"connect"}`, map[string]string{"Cookie": ""}, 401},
		{"POST", `{"action":"connect"}`, map[string]string{"Origin": "https://foreign.test"}, 403},
		{"POST", `{"action":"connect"}`, map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"POST", `action=connect`, map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, 415},
		{"POST", `{"action":"connect","account":"someone@example.com"}`, nil, 400},
		{"POST", `{"action":"connect"} {"action":"disconnect","confirm":true}`, nil, 400},
		{"POST", `connect`, nil, 400},
		{"PUT", `{"action":"connect"}`, nil, 405},
		{"DELETE", ``, nil, 405},
		{"GET", ``, map[string]string{"Cookie": ""}, 401},
	} {
		if got := relayStorageCall(h, tc.method, tc.body, tc.header); got.Code != tc.status {
			t.Fatalf("%s %s %v: %d %s, want %d", tc.method, tc.body, tc.header, got.Code, got.Body, tc.status)
		}
	}
	if f.reads != 0 || len(f.changes) != 0 {
		t.Fatalf("refused requests reached the provider: %d reads, %+v", f.reads, f.changes)
	}
	for _, body := range []string{"", `{"action":"connect"}`} {
		if got := relayStorageCall(h, "GET", body, nil); got.Code != 200 {
			t.Fatalf("read: %d %s", got.Code, got.Body)
		}
	}
	if f.reads != 2 || len(f.changes) != 0 {
		t.Fatalf("a read changed storage: %d reads, %+v", f.reads, f.changes)
	}
	if got := relayStorageCall(h, "POST", `{"action":"move","confirm":true}`, nil); got.Code != 200 || len(f.changes) != 1 || f.changes[0] != (drivecontract.RelayStorageAction{Action: "move", Confirm: true}) {
		t.Fatalf("change: %d %s %+v", got.Code, got.Body, f.changes)
	}
	unsupported := New(f.Fixture, relayStorageHost, testToken).Handler()
	for _, method := range []string{"GET", "POST"} {
		if got := relayStorageCall(unsupported, method, `{"action":"connect"}`, nil); got.Code != 404 {
			t.Fatalf("unsupported provider %s: %d %s", method, got.Code, got.Body)
		}
	}
}

// The browser engine's route: testdata/relay_storage_engine_check.mjs.
func TestRelayStorageBrowserEngine(t *testing.T) {
	runNodeCheck(t, "testdata/relay_storage_engine_check.mjs")
}

// Through a real local Hub: a member reads the relay's status without admin
// detail or a consent link, and the Hub refuses that member's changes; the
// admin's changes reach the Hub, whose own refusals reach the page in its
// words. No Google OAuth is configured, so nothing leaves this machine.
func TestRelayStorageLiveHubDecidesAdmin(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	alice, err := client.Join(ctx, filepath.Join(t.TempDir(), "alice"), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { alice.Close() })
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := client.Join(ctx, filepath.Join(t.TempDir(), "bob"), code, "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	admin, member := New(NewLive(alice), relayStorageHost, testToken).Handler(), New(NewLive(bob), relayStorageHost, testToken).Handler()
	read := func(h http.Handler) (drivecontract.RelayStorageStatus, string) {
		t.Helper()
		got := relayStorageCall(h, "GET", "", nil)
		var v drivecontract.RelayStorageStatus
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &v) != nil {
			t.Fatalf("read: %d %s", got.Code, got.Body)
		}
		return v, got.Body.String()
	}
	refused := func(h http.Handler, body, words string) {
		t.Helper()
		if got := relayStorageCall(h, "POST", body, nil); got.Code != 409 || strings.TrimSpace(got.Body.String()) != words {
			t.Fatalf("%s: %d %q, want 409 %q", body, got.Code, got.Body, words)
		}
	}
	m, raw := read(member)
	if m.CanAdmin || m.State != "not_configured" || m.Account != nil || m.Folder != nil || strings.Contains(raw, "consent_url") {
		t.Fatalf("member read: %s", raw)
	}
	for _, body := range []string{`{"action":"connect"}`, `{"action":"move","confirm":true}`, `{"action":"disconnect","confirm":true}`} {
		refused(member, body, "Only an admin of your server can change workspace attachment storage.")
	}
	first, _ := read(admin)
	again, raw := read(admin)
	if !first.CanAdmin || first.State != "not_configured" || strings.Contains(raw, "consent_url") || first != again {
		t.Fatalf("admin reads: %+v then %s", first, raw)
	}
	refused(admin, `{"action":"delete"}`, "Choose connect, move or disconnect.")
	refused(admin, `{"action":"move"}`, "Confirm moving existing encrypted attachments")
	refused(admin, `{"action":"connect"}`, "The relay operator must configure a Google Web OAuth client and the relay HTTPS callback first")
	if after, _ := read(admin); after != first {
		t.Fatalf("refused changes altered the relay: %+v then %+v", first, after)
	}
}
