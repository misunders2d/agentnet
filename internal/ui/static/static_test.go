package static

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strings"
	"testing"
)

func serve(method, target string, header ...string) *http.Response {
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	Relay().ServeHTTP(w, r)
	return w.Result()
}

// securityHeaders must be on every answer, found or not.
func securityHeaders(t *testing.T, what string, resp *http.Response) {
	t.Helper()
	for k, want := range map[string]string{
		"Content-Security-Policy":      relayCSP,
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"X-Frame-Options":              "DENY",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s: %s = %q, want %q", what, k, got, want)
		}
	}
}

// The relay serves the browser page and exactly its files, each with its
// type, cache rule and the security headers.
func TestRelayServesThePage(t *testing.T) {
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'",
		"base-uri 'none'", "form-action 'none'"} {
		if !strings.Contains(relayCSP, want) {
			t.Errorf("CSP lacks %q", want)
		}
	}
	for p, name := range relayFiles {
		resp := serve("GET", p)
		body, _ := io.ReadAll(resp.Body)
		data, err := fs.ReadFile(Files, name)
		if err != nil || resp.StatusCode != 200 || string(body) != string(data) {
			t.Fatalf("%s: %d, %d bytes (%v)", p, resp.StatusCode, len(body), err)
		}
		securityHeaders(t, p, resp)
		ct, cache := resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control")
		switch {
		case p == "/":
			if ct != "text/html; charset=utf-8" || cache != "no-store" {
				t.Errorf("%s: %q %q", p, ct, cache)
			}
		case strings.HasSuffix(p, ".css"):
			if ct != "text/css; charset=utf-8" || cache != "no-cache" {
				t.Errorf("%s: %q %q", p, ct, cache)
			}
		default:
			if ct != "text/javascript; charset=utf-8" || cache != "no-cache" {
				t.Errorf("%s: %q %q", p, ct, cache)
			}
		}
		etag := resp.Header.Get("ETag")
		if etag == "" {
			t.Fatalf("%s: no ETag", p)
		}
		if again := serve("GET", p, "If-None-Match", etag); again.StatusCode != http.StatusNotModified {
			t.Errorf("%s: unchanged file answered %d", p, again.StatusCode)
		}
		head := serve("HEAD", p)
		if b, _ := io.ReadAll(head.Body); head.StatusCode != 200 || len(b) != 0 {
			t.Errorf("HEAD %s: %d, %d bytes", p, head.StatusCode, len(b))
		}
	}
}

// Nothing else is served: not the daemon's page or its API, not the relay's
// API (the relay routes it before this handler), no listings, no tricks.
func TestRelayServesNothingElse(t *testing.T) {
	for _, p := range []string{"/index.html", "/relay.html", "/assets/app.js", "/assets/lenses.js", "/assets/index.html",
		"/api/overview", "/events", "/v1/version", "/v1/agents", "/assets/", "/assets/vendor/", "/assets/vendor/age.mjs/",
		"/assets/../relay.html", "/assets/static.go", "/static.go", "/assets/wire.mjs.map", "/favicon.ico"} {
		resp := serve("GET", p)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s: %d", p, resp.StatusCode)
		}
		securityHeaders(t, p, resp)
	}
	for _, m := range []string{"POST", "PUT", "DELETE", "PATCH", "OPTIONS"} {
		resp := serve(m, "/")
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s /: %d %q", m, resp.StatusCode, resp.Header.Get("Allow"))
		}
		securityHeaders(t, m, resp)
	}
}

// Every file the page loads is one the relay serves: the page's links and
// scripts, and each module's imports (relative to where it is served). No
// module loads code any other way.
func TestRelayPageLoadsOnlyServedFiles(t *testing.T) {
	served := map[string]bool{}
	for p := range relayFiles {
		served[p] = true
	}
	page, _ := fs.ReadFile(Files, "relay.html")
	for _, m := range regexp.MustCompile(`(?:src|href)="([^"]+)"`).FindAllStringSubmatch(string(page), -1) {
		if !served[m[1]] {
			t.Errorf("relay.html loads %s, which is not served", m[1])
		}
	}
	imports := regexp.MustCompile(`(?m)^\s*(?:import|export)\b[^;]*?\bfrom\s+"([^"]+)"`)
	for p, name := range relayFiles {
		if !strings.HasSuffix(name, ".mjs") {
			continue
		}
		data, _ := fs.ReadFile(Files, name)
		if strings.Contains(string(data), "import(") {
			t.Errorf("%s imports dynamically", name)
		}
		for _, m := range imports.FindAllStringSubmatch(string(data), -1) {
			if target := path.Join(path.Dir(p), m[1]); !strings.HasPrefix(m[1], "./") || !served[target] {
				t.Errorf("%s imports %s (%s), which is not served", name, m[1], target)
			}
		}
	}
}

// The daemon's page is in the bundle too.
func TestDaemonPageInBundle(t *testing.T) {
	for _, name := range []string{"index.html", "app.js", "lenses.js", "app.css"} {
		if _, err := fs.ReadFile(Files, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
