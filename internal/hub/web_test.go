package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserPageOptIn(t *testing.T) {
	h, id, addr := testHub(t)
	for _, enabled := range []bool{false, true} {
		h.cfg.Web = enabled
		handler := h.routes()
		request := func(method, path string) *httptest.ResponseRecorder {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest(method, path, nil))
			return w
		}
		want := http.StatusNotFound
		if enabled {
			want = http.StatusOK
		}
		for _, path := range []string{"/", "/assets/app.css", "/assets/wire.mjs"} {
			w := request(http.MethodGet, path)
			if w.Code != want {
				t.Fatalf("web=%t GET %s: %d, want %d", enabled, path, w.Code, want)
			}
			if enabled && (w.Header().Get("Content-Security-Policy") == "" || w.Header().Get("Referrer-Policy") != "no-referrer") {
				t.Fatalf("web=%t %s lost bundle security headers: %v", enabled, path, w.Header())
			}
		}
		if enabled {
			w := request(http.MethodGet, "/")
			if !strings.Contains(w.Header().Get("Content-Type"), "text/html") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("page headers: %v", w.Header())
			}
			if w := request(http.MethodHead, "/"); w.Code != http.StatusOK || w.Body.Len() != 0 {
				t.Fatalf("HEAD page: %d, %d bytes", w.Code, w.Body.Len())
			}
			if w := request(http.MethodPost, "/"); w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("POST page: %d", w.Code)
			}
		}
		// The bundle must not serve a daemon API, private files or arbitrary
		// assets. More-specific signed Hub routes keep their own authority.
		for _, path := range []string{"/api/overview", "/api/send", "/api/act", "/hub.db", "/bootstrap-invite.txt", "/assets/not-a-file.js"} {
			if w := request(http.MethodGet, path); w.Code != http.StatusNotFound {
				t.Fatalf("web=%t exposed %s: %d", enabled, path, w.Code)
			}
		}
		if w := request(http.MethodPost, "/api/send"); w.Code != http.StatusNotFound {
			t.Fatalf("web=%t exposed local send API: %d", enabled, w.Code)
		}
		if w := request(http.MethodGet, "/v1/version"); w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
			t.Fatalf("web=%t version route: %d %s", enabled, w.Code, w.Body)
		}
		if w := request(http.MethodGet, "/v1/agents"); w.Code != http.StatusUnauthorized {
			t.Fatalf("web=%t unsigned directory: %d", enabled, w.Code)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, signed(t, id, addr, http.MethodGet, "/v1/agents", nil))
		if w.Code != http.StatusOK {
			t.Fatalf("web=%t signed directory: %d %s", enabled, w.Code, w.Body)
		}
	}
}
