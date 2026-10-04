package ui

import (
	"net/http"
	"os"
	"path"
)

// The bundled app (static/default.html, app.js, lenses.js, app.css) is no
// longer shipped or served: no interface loads it. Its files stay in the
// source tree for the tests that exercise them, which serve them to their
// own fixture pages with serveBundledApp.
func serveBundledApp(w http.ResponseWriter, r *http.Request) bool {
	types := map[string]string{"/assets/app.js": "text/javascript; charset=utf-8", "/assets/lenses.js": "text/javascript; charset=utf-8", "/assets/app.css": "text/css; charset=utf-8"}
	ct, ok := types[r.URL.Path]
	if !ok {
		return false
	}
	data, err := os.ReadFile(path.Join("static", path.Base(r.URL.Path)))
	if err != nil {
		http.NotFound(w, r)
		return true
	}
	w.Header().Set("Content-Type", ct)
	w.Write(data)
	return true
}
