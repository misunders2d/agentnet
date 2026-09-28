// Package static is the messenger's page bundle, embedded in the binary, and
// the one handler a relay needs to serve its browser page. It depends on
// nothing else in AgentNet, so a relay can serve it without the client.
package static

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed index.html app.js lenses.js app.css relay.html relay.mjs wire.mjs vendor/age.mjs
var files embed.FS

// Files is the bundle: the daemon's page (index.html and its assets) and the
// relay's browser page (relay.html, relay.mjs, wire.mjs, vendor/age.mjs).
var Files fs.FS = files

// relayFiles are the only paths Relay serves, and the file behind each.
var relayFiles = map[string]string{
	"/":                      "relay.html",
	"/assets/app.css":        "app.css",
	"/assets/relay.mjs":      "relay.mjs",
	"/assets/wire.mjs":       "wire.mjs",
	"/assets/vendor/age.mjs": "vendor/age.mjs",
}

// relayCSP lets the page run only its own origin's files and talk only to
// its own origin.
const relayCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// Relay serves the browser page and its files on a relay's origin, for GET
// and HEAD, and nothing else: any other path is not found, so it never
// answers for the relay's API or the daemon's page API. Every answer carries
// headers that keep the page to its own origin's code and out of frames and
// other windows. They do not protect against the relay itself: whoever runs
// it can serve other code (MESSENGER_ARCHITECTURE §6).
func Relay() http.Handler {
	etags := map[string]string{}
	for _, name := range relayFiles {
		data, err := fs.ReadFile(files, name)
		if err != nil {
			panic(err) // a bundle file the list names is missing: a build error
		}
		sum := sha256.Sum256(data)
		etags[name] = `"` + hex.EncodeToString(sum[:16]) + `"`
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", relayCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		name, ok := relayFiles[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, _ := fs.ReadFile(files, name)
		h.Set("Content-Type", contentType(name))
		h.Set("ETag", etags[name])
		if name == "relay.html" {
			h.Set("Cache-Control", "no-store") // an updated relay serves its new page at once
		} else {
			h.Set("Cache-Control", "no-cache") // checked on every load (ETag)
		}
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
	})
}

func contentType(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		return "text/css; charset=utf-8"
	}
	return "text/javascript; charset=utf-8"
}
