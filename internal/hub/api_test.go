package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/identity"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func testHub(t *testing.T) (*Hub, *identity.Identity, string) {
	t.Helper()
	h, err := Open(Config{DataDir: filepath.Join(t.TempDir(), "hub"), PublicURL: "https://127.0.0.1:1", Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.Close() })
	id, _ := identity.Generate()
	addr := "admin/test"
	if err := h.store.createInvite("s", "admin", false, time.Hour, "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.enroll("s", id.Public(addr), "admin"); err != nil {
		t.Fatal(err)
	}
	return h, id, addr
}

func signed(t *testing.T, id *identity.Identity, addr, method, path string, body []byte) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	protocol.SignRequest(r, addr, id.Sign, body)
	return r
}

func serve(h *Hub, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.routes().ServeHTTP(w, r)
	return w
}

func TestRequestAuthentication(t *testing.T) {
	h, id, addr := testHub(t)
	path := "/v1/agents/admin/test"

	r := signed(t, id, addr, "GET", path, nil)
	replay := r.Clone(r.Context())
	if w := serve(h, r); w.Code != http.StatusOK {
		t.Fatalf("valid request: %d %s", w.Code, w.Body)
	}
	replay.Body = http.NoBody
	if w := serve(h, replay); w.Code != http.StatusUnauthorized {
		t.Fatalf("replayed request accepted: %d", w.Code)
	}

	body, _ := json.Marshal(protocol.RevokeRequest{Address: "x/y"})
	r = signed(t, id, addr, "POST", "/v1/admin/revoke", body)
	r.Body = http.NoBody // body swapped after signing
	if w := serve(h, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("body substitution accepted: %d", w.Code)
	}

	r = signed(t, id, addr, "GET", path, nil)
	r.URL.Path = "/v1/agents/admin/other"
	r.RequestURI = ""
	if w := serve(h, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("path substitution accepted: %d", w.Code)
	}

	r = signed(t, id, addr, "GET", path, nil)
	r.Header.Set(protocol.HeaderTime, strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10))
	if w := serve(h, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("stale request accepted: %d", w.Code)
	}

	other, _ := identity.Generate()
	r = signed(t, other, addr, "GET", path, nil)
	if w := serve(h, r); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key accepted: %d", w.Code)
	}
}

func TestNonAdminCannotInvite(t *testing.T) {
	h, id, addr := testHub(t) // enrolled without admin rights
	body, _ := json.Marshal(protocol.InviteRequest{Label: "eve"})
	if w := serve(h, signed(t, id, addr, "POST", "/v1/admin/invites", body)); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin invite: %d", w.Code)
	}
}

func TestEnrollmentReplay(t *testing.T) {
	h, _, _ := testHub(t)
	if err := h.store.createInvite("inv", "bob", false, time.Hour, "admin/test"); err != nil {
		t.Fatal(err)
	}
	id, _ := identity.Generate()
	join := func(id *identity.Identity) int {
		req := protocol.JoinRequest{Secret: "inv", Public: id.Public("bob/laptop")}
		protocol.SignJoin(&req, id.Sign)
		body, _ := json.Marshal(req)
		return serve(h, httptest.NewRequest("POST", "/v1/join", bytes.NewReader(body))).Code
	}
	if c := join(id); c != http.StatusCreated {
		t.Fatalf("first join: %d", c)
	}
	if c := join(id); c != http.StatusCreated {
		t.Fatalf("exact replay: %d", c)
	}
	other, _ := identity.Generate()
	if c := join(other); c != http.StatusForbidden {
		t.Fatalf("invite reuse with other key: %d", c)
	}
}

func TestPingAckOnlyForOwnLiveConnection(t *testing.T) {
	h, id, addr := testHub(t)
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	sub := h.streams.add(addr, cancel)
	ack := func(conn string) int {
		body, _ := json.Marshal(protocol.PingAck{Conn: conn})
		return serve(h, signed(t, id, addr, "POST", "/v1/stream/ack", body)).Code
	}
	if c := ack(sub.id); c != http.StatusNoContent {
		t.Fatalf("own live connection: %d", c)
	}
	h.streams.remove(addr, sub)
	if c := ack(sub.id); c != http.StatusNotFound {
		t.Fatalf("closed connection renewed: %d", c)
	}
	other := enrollOther(t, h)
	sub2 := h.streams.add(other, cancel)
	if c := ack(sub2.id); c != http.StatusNotFound {
		t.Fatalf("another agent's connection renewed: %d", c)
	}
}

func enrollOther(t *testing.T, h *Hub) string {
	t.Helper()
	id, _ := identity.Generate()
	if err := h.store.createInvite("o", "other", false, time.Hour, "admin/test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.enroll("o", id.Public("other/x"), "other"); err != nil {
		t.Fatal(err)
	}
	return "other/x"
}
