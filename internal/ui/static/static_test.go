package static

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/png"
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
		"base-uri 'none'", "form-action 'none'", "manifest-src 'self'", "worker-src 'self'"} {
		if !strings.Contains(relayCSP, want) {
			t.Errorf("CSP lacks %q", want)
		}
	}
	want := map[string][]byte{}
	for p, name := range relayFiles {
		data, err := devicePage(), error(nil)
		if name != "" {
			data, err = fs.ReadFile(Files, name)
		}
		if err != nil {
			t.Fatal(err)
		}
		want[p] = data
	}
	for p, size := range relayIcons {
		want[p] = AppIcon(size)
	}
	for p, data := range want {
		resp := serve("GET", p)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || string(body) != string(data) {
			t.Fatalf("%s: %d, %d bytes", p, resp.StatusCode, len(body))
		}
		securityHeaders(t, p, resp)
		ct, cache := resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control")
		switch {
		case p == "/":
			if ct != "text/html; charset=utf-8" || cache != "no-store" {
				t.Errorf("%s: %q %q", p, ct, cache)
			}
		case strings.HasSuffix(p, ".html"):
			if ct != "text/html; charset=utf-8" || cache != "no-cache" {
				t.Errorf("%s: %q %q", p, ct, cache)
			}
		case strings.HasSuffix(p, ".css"):
			if ct != "text/css; charset=utf-8" || cache != "no-cache" {
				t.Errorf("%s: %q %q", p, ct, cache)
			}
		case strings.HasSuffix(p, ".webmanifest"):
			if ct != "application/manifest+json" || cache != "no-cache" {
				t.Errorf("%s: %q %q", p, ct, cache)
			}
		case strings.HasSuffix(p, ".png"):
			if ct != "image/png" || cache != "no-cache" {
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
	for _, p := range []string{"/index.html", "/relay.html", "/assets/relay.mjs", "/assets/index.html",
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
	for p := range relayContent() {
		served[p] = true
	}
	page := devicePage()
	if strings.Contains(string(page), `src="/assets/app.js"`) || !strings.Contains(string(page), `<script type="module" src="/assets/device.mjs">`) {
		t.Fatal("the device page does not start with device.mjs")
	}
	for _, m := range regexp.MustCompile(`(?:src|href)="(/[^"]*)"`).FindAllStringSubmatch(string(page), -1) { // paths; #anchors stay in the page
		if !served[m[1]] {
			t.Errorf("the device page loads %s, which is not served", m[1])
		}
	}
	// device.mjs loads the views by address once the device is ready.
	dev, _ := fs.ReadFile(Files, "device.mjs")
	for _, m := range regexp.MustCompile(`"(/assets/[^"]+)"`).FindAllStringSubmatch(string(dev), -1) {
		if !served[m[1]] {
			t.Errorf("device.mjs loads %s, which is not served", m[1])
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

// The daemon's page is in the bundle too: the host and the built-in skin
// packages. The bundled app's files (default.html, app.js, lenses.js,
// app.css) stay in the source tree for their tests and are not shipped:
// no interface reaches them.
func TestDaemonPageInBundle(t *testing.T) {
	for _, name := range []string{"index.html", "loader.js", "core.css", "skin-base.css", "skinbar.mjs", "skinbar.css", "local-skins.mjs", "skins/comic/skin.json", "skins/comic/entry.mjs"} {
		if _, err := fs.ReadFile(Files, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, name := range []string{"default.html", "app.js", "lenses.js", "app.css", "messenger.mjs", "messenger.css", "m"} {
		if _, err := fs.Stat(Files, name); err == nil {
			t.Errorf("%s is shipped, but no interface may load it", name)
		}
	}
	for _, p := range []string{"/assets/default.html", "/assets/app.js", "/assets/lenses.js", "/assets/app.css", "/assets/messenger.mjs", "/assets/m/fonts/onest-latin-wght-normal.woff2"} {
		if resp := serve("GET", p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("relay serves %s: %d", p, resp.StatusCode)
		}
	}
}

// The manifest has what Chrome needs to offer an install (web.dev install
// criteria: name, 192 and 512 pixel icons, start_url, display,
// prefer_related_applications not true; no service worker), and each icon
// it names is served at its stated size.
func TestRelayManifest(t *testing.T) {
	resp := serve("GET", "/manifest.webmanifest")
	var m struct {
		ID, Name, ShortName, StartURL, Scope, Display string
		ShortNameJSON                                 string `json:"short_name"`
		StartURLJSON                                  string `json:"start_url"`
		Prefer                                        *bool  `json:"prefer_related_applications"`
		Icons                                         []struct{ Src, Sizes, Type string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "AgentNet" || m.ShortNameJSON == "" || m.StartURLJSON != "/" || m.Scope != "/" || m.Display != "standalone" ||
		m.Prefer == nil || *m.Prefer {
		t.Fatalf("manifest: %+v", m)
	}
	sizes := map[string]bool{}
	for _, icon := range m.Icons {
		r := serve("GET", icon.Src)
		data, _ := io.ReadAll(r.Body)
		cfg, err := png.DecodeConfig(bytes.NewReader(data))
		if r.StatusCode != 200 || err != nil || icon.Type != "image/png" || icon.Sizes != fmt.Sprintf("%dx%d", cfg.Width, cfg.Height) {
			t.Fatalf("icon %+v: %d %v %+v", icon, r.StatusCode, err, cfg)
		}
		sizes[icon.Sizes] = true
	}
	if !sizes["192x192"] || !sizes["512x512"] {
		t.Fatalf("icon sizes: %v", sizes)
	}
	if page := string(devicePage()); strings.Count(page, `rel="manifest"`) != 1 || !strings.Contains(page, `<link rel="manifest" href="/manifest.webmanifest" crossorigin="use-credentials">`) {
		t.Fatal("the device page does not share one credentialed manifest link")
	}
	if page := string(devicePage()); strings.Count(page, `rel="icon"`) != 1 || !strings.Contains(page, `<link rel="icon" type="image/png" href="/assets/icon-192.png">`) {
		t.Fatal("the device page does not name its icon once")
	}
	// The icon is the ant (dark teal) on a light tile, with clear corners
	// and room around the ant, at each size.
	for _, size := range []int{192, 512} {
		img, err := png.Decode(bytes.NewReader(AppIcon(size)))
		if err != nil || img.Bounds().Dx() != size || img.Bounds().Dy() != size {
			t.Fatalf("icon %d: %v %v", size, err, img.Bounds())
		}
		at := func(fx, fy float64) [4]uint32 {
			r, g, b, a := img.At(int(fx*float64(size)), int(fy*float64(size))).RGBA()
			return [4]uint32{r >> 8, g >> 8, b >> 8, a >> 8}
		}
		if c := at(0, 0); c[3] != 0 {
			t.Errorf("%d: corner not clear: %v", size, c)
		}
		for _, p := range [][2]float64{{.5, .06}, {.06, .5}, {.94, .5}, {.5, .94}} { // the margin: tile only
			if c := at(p[0], p[1]); c != [4]uint32{iconTileR, iconTileG, iconTileB, 255} {
				t.Errorf("%d: margin at %v: %v", size, p, c)
			}
		}
		if c := at(.5, .56); c[3] != 255 || c[0] > 40 || c[1] > 90 || c[2] > 100 { // the ant's middle
			t.Errorf("%d: ant: %v", size, c)
		}
	}
}
