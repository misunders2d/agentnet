package static

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// ValidateBrowserOrigins permits only explicit canonical HTTPS origins. The
// allowlist is administrative routing consent, never membership or authority.
func ValidateBrowserOrigins(origins []string) ([]string, error) {
	if len(origins) > 32 {
		return nil, errors.New("at most 32 browser origins allowed")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Opaque != "" || strings.ContainsAny(u.Host, "* \t\r\n;,'\"") || u.Host != strings.ToLower(u.Host) || u.Port() == "443" {
			return nil, errors.New("browser origin must be a canonical HTTPS origin without path, credentials, wildcard or default port")
		}
		if origin != "https://"+u.Host {
			return nil, errors.New("browser origin must be canonical")
		}
		if !seen[origin] {
			out = append(out, origin)
			seen[origin] = true
		}
	}
	return out, nil
}

// WithConnectOrigins extends only CSP connect-src. Scripts and styling stay
// same-origin; a dynamic invite cannot relax this administrative allowlist.
func WithConnectOrigins(next http.Handler, origins []string) (http.Handler, error) {
	approved, err := ValidateBrowserOrigins(origins)
	if err != nil {
		return nil, err
	}
	if len(approved) == 0 {
		return next, nil
	}
	replacement := "connect-src 'self' " + strings.Join(approved, " ") + ";"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&workspaceCSPWriter{ResponseWriter: w, replacement: replacement}, r)
	}), nil
}

type workspaceCSPWriter struct {
	http.ResponseWriter
	replacement string
	wrote       bool
}

func (w *workspaceCSPWriter) apply() {
	if w.wrote {
		return
	}
	w.wrote = true
	h := w.Header()
	h.Set("Content-Security-Policy", strings.Replace(h.Get("Content-Security-Policy"), "connect-src 'self';", w.replacement, 1))
}
func (w *workspaceCSPWriter) WriteHeader(status int) { w.apply(); w.ResponseWriter.WriteHeader(status) }
func (w *workspaceCSPWriter) Write(p []byte) (int, error) {
	w.apply()
	return w.ResponseWriter.Write(p)
}
func (w *workspaceCSPWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
