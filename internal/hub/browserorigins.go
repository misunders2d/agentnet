package hub

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/ui/static"
)

// WithBrowserOrigins enables only configured browser API origins. Actual
// requests still go through every existing signature and membership check.
func WithBrowserOrigins(next http.Handler, ownOrigin string, origins []string) (http.Handler, error) {
	approved, err := static.ValidateBrowserOrigins(origins)
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, origin := range approved {
		allowed[origin] = true
	}
	own, err := url.Parse(ownOrigin)
	if err != nil {
		return nil, err
	}
	ownOrigin = own.Scheme + "://" + own.Host
	methods := map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true}
	headers := map[string]bool{"content-type": true, "range": true, "accept": true, "x-agentnet-agent": true, "x-agentnet-time": true, "x-agentnet-nonce": true, "x-agentnet-sig": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin == "" || origin == ownOrigin {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		if !allowed[origin] {
			http.Error(w, "browser origin not approved", http.StatusForbidden)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		if r.Method == http.MethodOptions {
			w.Header().Add("Vary", "Access-Control-Request-Method")
			w.Header().Add("Vary", "Access-Control-Request-Headers")
			if !methods[r.Header.Get("Access-Control-Request-Method")] {
				http.Error(w, "browser method not approved", 403)
				return
			}
			for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				header = strings.ToLower(strings.TrimSpace(header))
				if header != "" && !headers[header] {
					http.Error(w, "browser header not approved", 403)
					return
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Range, Accept, X-Agentnet-Agent, X-Agentnet-Time, X-Agentnet-Nonce, X-Agentnet-Sig")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !methods[r.Method] {
			http.Error(w, "browser method not approved", 403)
			return
		}
		w.Header().Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, "+protocol.MembersHeader+", "+protocol.TeamsHeader+", "+protocol.SignalsHeader)
		next.ServeHTTP(w, r)
	}), nil
}
