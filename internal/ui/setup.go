package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/misunders2d/agentnet/internal/ui/static"
)

// The AgentNet app's first-run page (static/setup.mjs), served by the app
// (cmd/agentnet app.go) on its own address while this computer has not
// joined yet: the same Host, token, cookie and origin checks as the
// messenger page (guard), so the app's window keeps its session when the
// messenger page takes the same address over after the join.

// SetupView is what the first-run page shows on opening.
type SetupView struct {
	// State is none (nothing joined here yet), incomplete (a join that
	// did not finish: the same invitation again completes it), or how this
	// computer's membership ended: refused (its device link was refused on
	// the other device), expired (nobody approved it in time) or removed
	// (its server removed it). An ended computer starts again only after
	// the person asks to (SetupStartAgain).
	State string `json:"state"`
	// Device is the name this computer will have, made automatically, and
	// DeviceWords the same in words ("Linux laptop").
	Device      string `json:"device"`
	DeviceWords string `json:"device_words"`
}

// SetupInvite is what an invitation or device link the person pasted or
// opened says. Host is the server it is for, from the code; Name, From and
// Workspace are only what the inviter wrote on it (unsigned).
type SetupInvite struct {
	Kind      string `json:"kind"` // invite or link
	Host      string `json:"host"`
	Name      string `json:"name,omitempty"`
	From      string `json:"from,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Expires   string `json:"expires,omitempty"` // a device link's end (RFC 3339)
	Problem   string `json:"problem,omitempty"` // why it cannot be used here, in plain words
}

type SetupGoogleStatus struct {
	State   string `json:"state"`
	Problem string `json:"problem,omitempty"`
}
type SetupGoogleProvider interface {
	SetupGoogle(hub string) error
	SetupGoogleState() (SetupGoogleStatus, <-chan struct{})
}

// SetupJoin is the person's Join: the code, and with an invitation the name
// people will see.
type SetupJoin struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// SetupResult answers a join once the messenger page serves this address.
type SetupResult struct {
	Host string `json:"host"`
	// Waiting is the device that must approve this computer (a device
	// link), or "".
	Waiting string `json:"waiting,omitempty"`
}

// SetupProvider is the app's first-run side.
type SetupProvider interface {
	SetupState() SetupView
	SetupInspect(code string) SetupInvite
	// SetupJoin joins with code and answers only once the messenger page
	// serves this address. Refusals are Refuse errors in plain words.
	SetupJoin(j SetupJoin) (SetupResult, error)
	// SetupStartAgain, on the person's click in an ended state, keeps
	// what this computer had aside (never deleted) and answers the state
	// it starts from: none.
	SetupStartAgain() (SetupView, error)
}

// NewSetup is the first-run page for p on host, authenticated by token.
func NewSetup(p SetupProvider, host, token string) http.Handler {
	s := &Server{host: host, token: token, restarting: make(chan struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { s.serveHTML(w, r, static.SetupPage()) })
	mux.HandleFunc("GET /assets/{name}", func(w http.ResponseWriter, r *http.Request) {
		switch r.PathValue("name") {
		case "core.css", "landing.css", "setup.mjs", "icon-192.png", "icon-512.png":
			s.asset(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("GET /favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /api/setup", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, p.SetupState()) })
	mux.HandleFunc("POST /api/setup/google", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Hub string `json:"hub"`
		}
		if !readJSON(w, r, &v) {
			return
		}
		g, ok := p.(SetupGoogleProvider)
		if !ok {
			http.Error(w, "Google sign-in unavailable", 404)
			return
		}
		writeResult(w, struct{}{}, g.SetupGoogle(v.Hub))
	})
	mux.HandleFunc("POST /api/setup/google/cancel", func(w http.ResponseWriter, r *http.Request) {
		g, ok := p.(interface{ SetupGoogleCancel() error })
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeResult(w, struct{}{}, g.SetupGoogleCancel())
	})
	mux.HandleFunc("GET /api/setup/google/events", func(w http.ResponseWriter, r *http.Request) {
		g, ok := p.(SetupGoogleProvider)
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		v, done := g.SetupGoogleState()
		if v.State == "waiting" || v.State == "" {
			select {
			case <-r.Context().Done():
				return
			case <-done:
			}
			v, _ = g.SetupGoogleState()
		}
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		http.NewResponseController(w).Flush()
	})
	mux.HandleFunc("POST /api/setup/inspect", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Code string `json:"code"`
		}
		if readJSON(w, r, &v) {
			writeJSON(w, p.SetupInspect(v.Code))
		}
	})
	mux.HandleFunc("POST /api/setup/join", func(w http.ResponseWriter, r *http.Request) {
		var v SetupJoin
		if readJSON(w, r, &v) {
			res, err := p.SetupJoin(v)
			writeResult(w, res, err)
		}
	})
	mux.HandleFunc("POST /api/setup/start-again", func(w http.ResponseWriter, r *http.Request) {
		var v struct{}
		if readJSON(w, r, &v) {
			res, err := p.SetupStartAgain()
			writeResult(w, res, err)
		}
	})
	return s.guard(mux)
}

const tokenHandoffScript = `location.replace("/"+location.hash);`

// serveHTML serves a page. Arriving with the token sets the cookie and
// commits a same-origin document before replacing it with the tokenless page.
// A HTTP redirect from the native splash keeps a cross-site initiator and
// withholds the Strict cookie. The replacement keeps deep-link fragments and
// removes the token entry from history; only this fixed script may run.
func (s *Server) serveHTML(w http.ResponseWriter, r *http.Request, data []byte) {
	if r.URL.Query().Has("t") {
		http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/",
			HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		sum := sha256.Sum256([]byte(tokenHandoffScript))
		policy := strings.Replace(w.Header().Get("Content-Security-Policy"), "script-src 'self'", "script-src 'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'", 1)
		w.Header().Set("Content-Security-Policy", policy)
		fmt.Fprintf(w, "<!doctype html><script>%s</script>", tokenHandoffScript)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache") // an updated daemon serves a new page at the same address
	w.Write(data)
}
