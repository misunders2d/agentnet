package static

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleCSPPreservesWorkspaceOrigins(t *testing.T) {
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", relayCSP)
		w.Write([]byte("page"))
	})
	workspace, err := WithConnectOrigins(base, []string{"https://shell.example"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRecorder()
	WithGoogleSignIn(workspace).ServeHTTP(r, httptest.NewRequest("GET", "https://relay.example/", nil))
	csp := r.Header().Get("Content-Security-Policy")
	for _, want := range []string{"script-src 'self' https://accounts.google.com/gsi/client;", "connect-src 'self' https://accounts.google.com/gsi/ https://shell.example;", "frame-src https://accounts.google.com/gsi/;", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("missing %s in %s", want, csp)
		}
	}
	if strings.Contains(csp, "*") || strings.Contains(csp, "unsafe-inline") {
		t.Fatal("CSP weakened")
	}
	if r.Header().Get("Cross-Origin-Opener-Policy") != "same-origin-allow-popups" {
		t.Fatal("GIS popup policy")
	}
}
