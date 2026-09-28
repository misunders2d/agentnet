package ui

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"strings"
	"sync"
)

//go:embed static
var static embed.FS

const (
	cookieName = "agentnet_ui"
	maxBody    = 64 << 10
)

// Server serves the page and its API for one Provider.
type Server struct {
	p          Provider
	host       string // the exact Host header accepted, e.g. 127.0.0.1:43127
	token      string
	restarting chan struct{} // closed when the daemon stops to switch programs
	once       sync.Once
}

// New returns a server that accepts only requests addressed to host (the
// listener's address) and authenticated by token: once as ?t= on the page,
// which sets an HttpOnly cookie, then by that cookie.
func New(p Provider, host, token string) *Server {
	return &Server{p: p, host: host, token: token, restarting: make(chan struct{})}
}

// Restarting tells open pages, with a content-free event, that the daemon is
// stopping to switch to an updated program and will serve them again at the
// same address. The page then reconnects for a bounded time.
func (s *Server) Restarting() { s.once.Do(func() { close(s.restarting) }) }

// Handler is the whole site.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /assets/{name}", s.asset)
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/overview", s.overview)
	mux.HandleFunc("GET /api/thread", s.thread)
	mux.HandleFunc("POST /api/refresh", s.refresh)
	mux.HandleFunc("POST /api/send", s.send)
	mux.HandleFunc("POST /api/act", s.act)
	mux.HandleFunc("POST /api/simulate", s.simulate)
	mux.HandleFunc("GET /events", s.events)
	return s.guard(mux)
}

// guard applies the checks every request must pass: the Host must be the
// loopback address we listen on (DNS rebinding), a session cookie or the
// token on the page must match, and state-changing requests must
// come from this origin as JSON (cross-site requests and forms).
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; "+
			"img-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if r.Host != s.host {
			http.Error(w, "wrong host", http.StatusMisdirectedRequest)
			return
		}
		if !s.authed(r) {
			http.Error(w, "open the address agentnet printed", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if r.Header.Get("Origin") != "http://"+s.host {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			if mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mt != "application/json" {
				http.Error(w, "JSON only", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authed(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && same(c.Value, s.token) {
		return true
	}
	return r.Method == http.MethodGet && r.URL.Path == "/" && same(r.URL.Query().Get("t"), s.token)
}

func same(a, b string) bool {
	return b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// page serves index.html. Arriving with the token sets the cookie and
// redirects so the token leaves the address bar and history.
func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("t") {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	data, err := static.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache") // an updated daemon serves a new page at the same address
	w.Write(data)
}

// asset serves one embedded file; there are no directory listings.
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	types := map[string]string{"app.js": "text/javascript; charset=utf-8", "lenses.js": "text/javascript; charset=utf-8", "app.css": "text/css; charset=utf-8"}
	ct, ok := types[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(static, "static/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(data)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	o, err := s.p.Overview()
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, o)
}

func (s *Server) thread(w http.ResponseWriter, r *http.Request) {
	t, err := s.p.Thread(r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, t)
}

// refresh asks the network about one thread once, when the page opens it.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var v struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	rf, ok := s.p.(Refresher)
	if !ok {
		writeJSON(w, Presence{Text: "Connection unknown"})
		return
	}
	pr, err := rf.Refresh(v.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, pr)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var d Draft
	if !readJSON(w, r, &d) {
		return
	}
	m, err := s.p.Send(d)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, m)
}

func (s *Server) act(w http.ResponseWriter, r *http.Request) {
	var a Action
	if !readJSON(w, r, &a) {
		return
	}
	note, err := s.p.Act(a)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, struct {
		Note string `json:"note"`
	}{note})
}

func (s *Server) simulate(w http.ResponseWriter, r *http.Request) {
	sim, ok := s.p.(Simulator)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var v struct {
		What string `json:"what"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if err := sim.Simulate(v.What); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, struct{}{})
}

// events streams the change counter: one event now and one after each
// change. Events carry no content; the page fetches what it shows.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	for {
		seq, next := s.p.Changed()
		if _, err := fmt.Fprintf(w, "event: change\ndata: %d\n\n", seq); err != nil {
			return
		}
		fl.Flush()
		select {
		case <-next:
		case <-s.restarting:
			fmt.Fprint(w, "event: restart\ndata: \n\n")
			fl.Flush()
			return
		case <-r.Context().Done():
			return
		}
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("ui: write: %v", err)
	}
}

// writeErr answers with the provider's message for expected refusals and a
// generic one otherwise.
func writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, strings.TrimSpace(err.Error()), http.StatusNotFound)
	case errors.Is(err, ErrRefused):
		http.Error(w, strings.TrimSpace(err.Error()), http.StatusConflict)
	default:
		log.Printf("ui: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
	}
}
