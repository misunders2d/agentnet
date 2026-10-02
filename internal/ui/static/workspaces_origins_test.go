package static

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkspaceOriginsValidationAndCSP(t *testing.T) {
	for _, bad := range []string{"*", "null", "http://localhost:4000", "https://shell.example/", "https://user@host.example", "https://host.example?q=x", "https://host.example#x", "https://HOST.example", "https://host.example:443", "https://host.example;script-src"} {
		if _, err := ValidateBrowserOrigins([]string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	origins, err := ValidateBrowserOrigins([]string{"https://shell.example", "https://shell.example", "https://other.example:8443"})
	if err != nil || len(origins) != 2 {
		t.Fatalf("canonical origins %v %v", origins, err)
	}
	base := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", relayCSP)
		w.Write([]byte("page"))
	})
	wrapped, err := WithConnectOrigins(base, origins)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	wrapped.ServeHTTP(response, httptest.NewRequest("GET", "https://relay.example/", nil))
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "connect-src 'self' https://shell.example https://other.example:8443;") || !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "*") {
		t.Fatalf("CSP %s", csp)
	}
}
