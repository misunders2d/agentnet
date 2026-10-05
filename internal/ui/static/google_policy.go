package static

import (
	"net/http"
	"strings"
)

// WithGoogleSignIn permits only Google's documented GIS resources, and
// only when the relay operator configured browser sign-in. It grants no
// new API origin, wildcard, inline script or frame embedding permission.
func WithGoogleSignIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { next.ServeHTTP(&googleCSPWriter{ResponseWriter: w}, r) })
}

type googleCSPWriter struct {
	http.ResponseWriter
	wrote bool
}

func (w *googleCSPWriter) apply() {
	if w.wrote {
		return
	}
	w.wrote = true
	h := w.Header()
	c := h.Get("Content-Security-Policy")
	if c == "" {
		return
	}
	c = strings.Replace(c, "script-src 'self';", "script-src 'self' https://accounts.google.com/gsi/client;", 1)
	c = strings.Replace(c, "style-src 'self';", "style-src 'self' https://accounts.google.com/gsi/style;", 1)
	c = strings.Replace(c, "connect-src 'self'", "connect-src 'self' https://accounts.google.com/gsi/", 1)
	h.Set("Content-Security-Policy", c+"; frame-src https://accounts.google.com/gsi/;")
	h.Set("Cross-Origin-Opener-Policy", "same-origin-allow-popups")
}
func (w *googleCSPWriter) WriteHeader(s int)           { w.apply(); w.ResponseWriter.WriteHeader(s) }
func (w *googleCSPWriter) Write(b []byte) (int, error) { w.apply(); return w.ResponseWriter.Write(b) }
func (w *googleCSPWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
