package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestBrowserOriginsExactPreflightAndAuthentication(t *testing.T) {
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "signature required", 401) })
	handler, err := WithBrowserOrigins(next, "https://relay.example", []string{"https://shell.example"})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		origin, method, requestedMethod, headers string
		want                                     int
	}{
		{"https://shell.example", "OPTIONS", "POST", "content-type,x-agentnet-agent,x-agentnet-time,x-agentnet-nonce,x-agentnet-sig", 204},
		{"https://foreign.example", "OPTIONS", "POST", "content-type", 403},
		{"null", "OPTIONS", "GET", "", 403},
		{"https://shell.example", "OPTIONS", "PATCH", "content-type", 403},
		{"https://shell.example", "OPTIONS", "POST", "authorization", 403},
		{"https://shell.example", "POST", "", "", 401},
		{"https://relay.example", "POST", "", "", 401},
		{"", "GET", "", "", 401},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "https://relay.example/v1/messages", nil)
		req.Header.Set("Origin", c.origin)
		req.Header.Set("Access-Control-Request-Method", c.requestedMethod)
		req.Header.Set("Access-Control-Request-Headers", c.headers)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Fatalf("%+v: %d", c, rec.Code)
		}
		if rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("enabled browser credentials")
		}
		if allow := rec.Header().Get("Access-Control-Allow-Origin"); allow == "*" || allow != "" && allow != c.origin {
			t.Fatal("broad origin reflection")
		}
	}
	if calls != 3 {
		t.Fatalf("actual auth handler calls=%d", calls)
	}
}
func TestBrowserOriginsEmptyKeepsSameOrigin(t *testing.T) {
	handler, err := WithBrowserOrigins(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), "https://relay.example", nil)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "https://relay.example/v1/messages", nil)
	req.Header.Set("Origin", "https://shell.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Fatal("empty config opened foreign origins")
	}
}

func TestBrowserOriginsExposesStreamCapabilitiesWithoutOpeningAuthority(t *testing.T) {
	calls := 0
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Test-Authenticated") != "yes" {
			http.Error(w, "signature required", http.StatusUnauthorized)
			return
		}
		for _, header := range []string{protocol.MembersHeader, protocol.TeamsHeader, protocol.SignalsHeader} {
			w.Header().Set(header, "1")
		}
		w.WriteHeader(http.StatusOK)
	})
	handler, err := WithBrowserOrigins(next, "https://relay.example", []string{"https://shell.example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		origin        string
		authenticated bool
		status        int
	}{
		{"https://shell.example", true, http.StatusOK},
		{"https://shell.example", false, http.StatusUnauthorized},
		{"https://foreign.example", true, http.StatusForbidden},
	} {
		req := httptest.NewRequest("GET", "https://relay.example/v1/stream", nil)
		req.Header.Set("Origin", c.origin)
		if c.authenticated {
			req.Header.Set("X-Test-Authenticated", "yes")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != c.status {
			t.Fatalf("%+v status=%d", c, rec.Code)
		}
		exposed := rec.Header().Get("Access-Control-Expose-Headers")
		if strings.Contains(exposed, "*") || rec.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("broadened CORS authority")
		}
		if c.origin == "https://foreign.example" {
			if exposed != "" || rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("denied origin exposes response")
			}
			continue
		}
		want := "Content-Length, Content-Range, " + protocol.MembersHeader + ", " + protocol.TeamsHeader + ", " + protocol.SignalsHeader
		if exposed != want || rec.Header().Get("Access-Control-Allow-Origin") != c.origin {
			t.Fatalf("exposure=%q", exposed)
		}
		for _, header := range []string{protocol.MembersHeader, protocol.TeamsHeader, protocol.SignalsHeader} {
			value := rec.Header().Get(header)
			if c.authenticated && value != "1" {
				t.Fatalf("missing declared %s", header)
			}
			if !c.authenticated && value != "" {
				t.Fatalf("unauthenticated capability %s", header)
			}
		}
	}
	if calls != 2 {
		t.Fatalf("auth handler calls=%d", calls)
	}
}

// A browser workspace of an approved origin may send the client version
// (protocol.VersionHeader) with its signed requests: the preflight allows
// it and says so.
func TestBrowserOriginsAllowVersionHeader(t *testing.T) {
	handler, err := WithBrowserOrigins(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), "https://relay.example", []string{"https://shell.example"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("OPTIONS", "https://relay.example/v1/stream", nil)
	req.Header.Set("Origin", "https://shell.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "accept,agentnet-version,x-agentnet-agent,x-agentnet-nonce,x-agentnet-sig,x-agentnet-time")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent || !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), protocol.VersionHeader) {
		t.Fatalf("preflight with the version: %d, allowed %q", rec.Code, rec.Header().Get("Access-Control-Allow-Headers"))
	}
}
