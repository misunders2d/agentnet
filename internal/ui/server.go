package ui

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/misunders2d/agentnet/internal/ui/static"
)

const (
	cookieName = "agentnet_ui"
	maxBody    = 64 << 10
	maxUpload  = 101 << 20 // a file's bytes (the client refuses beyond its MaxFileSize)
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
	mux.HandleFunc("POST /api/person", s.person)
	mux.HandleFunc("POST /api/device/{what}", s.device)
	mux.HandleFunc("GET /api/dm", s.dm)
	mux.HandleFunc("POST /api/dm/new", s.newDM)
	mux.HandleFunc("POST /api/dm/send", s.sendDM)
	mux.HandleFunc("POST /api/dm/agent/invite", s.inviteAgent)
	mux.HandleFunc("POST /api/dm/agent/decide", s.decideAgent)
	mux.HandleFunc("POST /api/dm/agent/dismiss", s.dismissAgent)
	mux.HandleFunc("POST /api/dm/agent/ask", s.askAgent)
	mux.HandleFunc("POST /api/notify/{what}", s.notify)
	mux.HandleFunc("POST /api/upload", s.upload)
	mux.HandleFunc("POST /api/upload/discard", s.discard)
	mux.HandleFunc("GET /api/files/{id}/{i}", s.file)
	mux.HandleFunc("POST /api/remind", s.remind)
	mux.HandleFunc("POST /api/remind/{what}", s.remind)
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
			"img-src 'self' blob:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if r.Host != s.host {
			http.Error(w, "wrong host", http.StatusMisdirectedRequest)
			return
		}
		if !s.authed(r) {
			// A notification's click opens the page without its token and
			// relies on this browser's session; when that is gone, say how
			// to get in again.
			http.Error(w, "This page needs its address from this computer: run agentnet ui and open the address it prints.", http.StatusUnauthorized)
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
			mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
			limit := int64(maxBody)
			switch {
			case r.URL.Path == "/api/upload" && mt == "application/octet-stream": // a file's bytes, handed to this computer's AgentNet
				limit = maxUpload
			case mt != "application/json":
				http.Error(w, "JSON only", http.StatusUnsupportedMediaType)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
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
	data, err := fs.ReadFile(static.Files, "index.html")
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
	if name == "icon-192.png" { // the favicon and the header's mark
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-cache")
		w.Write(static.AppIcon(192))
		return
	}
	types := map[string]string{"app.js": "text/javascript; charset=utf-8", "lenses.js": "text/javascript; charset=utf-8", "app.css": "text/css; charset=utf-8"}
	ct, ok := types[name]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(static.Files, name)
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
	_, o.Agents = s.p.(Participants)
	writeJSON(w, o)
}

func (s *Server) participants(w http.ResponseWriter) (Participants, bool) {
	p, ok := s.p.(Participants)
	if !ok {
		writeErr(w, NotFound("agents cannot be invited into DMs here"))
	}
	return p, ok
}

func (s *Server) inviteAgent(w http.ResponseWriter, r *http.Request) {
	var d AgentInvite
	if !readJSON(w, r, &d) {
		return
	}
	if p, ok := s.participants(w); ok {
		v, err := p.InviteAgent(d)
		writeResult(w, v, err)
	}
}

func (s *Server) decideAgent(w http.ResponseWriter, r *http.Request) {
	var d struct {
		PID    string `json:"pid"`
		Accept bool   `json:"accept"`
	}
	if !readJSON(w, r, &d) {
		return
	}
	if p, ok := s.participants(w); ok {
		v, err := p.DecideAgent(d.PID, d.Accept)
		writeResult(w, v, err)
	}
}

func (s *Server) dismissAgent(w http.ResponseWriter, r *http.Request) {
	var d struct {
		PID string `json:"pid"`
	}
	if !readJSON(w, r, &d) {
		return
	}
	if p, ok := s.participants(w); ok {
		v, err := p.DismissAgent(d.PID)
		writeResult(w, v, err)
	}
}

func (s *Server) askAgent(w http.ResponseWriter, r *http.Request) {
	var d AgentAsk
	if !readJSON(w, r, &d) {
		return
	}
	if p, ok := s.participants(w); ok {
		v, err := p.AskAgent(d)
		writeResult(w, v, err)
	}
}

// notify serves the alert controls: enable, disable, mute, allow and
// seen (a presentation report).
func (s *Server) notify(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Alerts)
	if !ok {
		writeErr(w, NotFound("alerts are not available here"))
		return
	}
	var v struct {
		Conv    string   `json:"conv"`
		Person  string   `json:"person"`
		Muted   bool     `json:"muted"`
		Allowed bool     `json:"allowed"`
		IDs     []string `json:"ids"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	var note string
	var err error
	switch r.PathValue("what") {
	case "enable":
		note, err = p.NotifyEnable()
	case "disable":
		note, err = p.NotifyDisable()
	case "mute":
		note, err = p.NotifyMute(v.Conv, v.Muted)
	case "allow":
		note, err = p.NotifyAllow(v.Person, v.Allowed)
	case "seen":
		err = p.NotifySeen(v.Conv, v.IDs)
	default:
		err = NotFound("no such alert control")
	}
	writeResult(w, map[string]string{"note": note}, err)
}

// upload keeps the bytes of one file the page will send, privately, and
// answers the id the send names. The name is the person's file name, shown
// to the recipient (checked again when sent).
func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Files)
	if !ok {
		writeErr(w, NotFound("files are not available here"))
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		writeErr(w, Refuse("A file needs a name of at most 255 bytes, without control characters."))
		return
	}
	id, err := p.StageFile(name, r.Body)
	writeResult(w, map[string]string{"id": id}, err)
}

// discard removes files the page handed over and will not send (a file
// removed from a draft).
func (s *Server) discard(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Files)
	if !ok {
		writeErr(w, NotFound("files are not available here"))
		return
	}
	var v struct {
		IDs []string `json:"ids"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	if len(v.IDs) > maxDiscard {
		writeErr(w, Refuse("Too many files at once."))
		return
	}
	p.DiscardFiles(v.IDs)
	writeJSON(w, map[string]bool{"ok": true})
}

// filesHere refuses a send naming files where none can be sent (the demo),
// rather than sending it without them.
func (s *Server) filesHere(w http.ResponseWriter, n int) bool {
	if _, ok := s.p.(Files); n > 0 && !ok {
		writeErr(w, NotFound("files are not available here"))
		return false
	}
	return true
}

// maxDiscard bounds one discard: more than a page can have staged.
const maxDiscard = 64

// file serves a received file, checked and decrypted, only as a download:
// never a type the browser would run or render as a page.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Files)
	if !ok {
		writeErr(w, NotFound("files are not available here"))
		return
	}
	i, err := strconv.Atoi(r.PathValue("i"))
	if err != nil || i < 0 {
		writeErr(w, NotFound("no such file"))
		return
	}
	rc, name, err := p.OpenFile(r.Context(), r.PathValue("id"), i)
	if err != nil {
		writeErr(w, err)
		return
	}
	defer rc.Close()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	io.Copy(w, rc)
}

// remind sets (or moves) a reminder, or marks it done or cancels it.
func (s *Server) remind(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Reminders)
	if !ok {
		writeErr(w, NotFound("reminders are not available here"))
		return
	}
	var v struct {
		ID  string `json:"id"`
		Due int64  `json:"due"` // unix seconds
	}
	if !readJSON(w, r, &v) {
		return
	}
	var err error
	note := ""
	switch r.PathValue("what") {
	case "":
		err, note = p.SetReminder(v.ID, time.Unix(v.Due, 0)), "Reminder set."
	case "done":
		err, note = p.DoneReminder(v.ID), "Reminder done."
	case "cancel":
		err, note = p.CancelReminder(v.ID), "Reminder cancelled."
	default:
		err = NotFound("no such reminder control")
	}
	writeResult(w, map[string]string{"note": note}, err)
}

// writeResult writes v, or err as the page's error.
func writeResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, v)
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

// persons is the provider's human DMs, or a refusal saying there are none.
// device handles one person's devices (Identity): marking this
// installation a service, a link for a new device, deciding a new device's
// request, removing a device.
func (s *Server) device(w http.ResponseWriter, r *http.Request) {
	p, ok := s.p.(Identity)
	if !ok {
		writeErr(w, NotFound("devices are not available here"))
		return
	}
	var v struct {
		ID      string `json:"id"`
		Accept  bool   `json:"accept"`
		Address string `json:"address"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	switch r.PathValue("what") {
	case "service":
		note, err := p.SetService()
		writeResult(w, map[string]string{"note": note}, err)
	case "link":
		l, err := p.NewDeviceLink()
		writeResult(w, l, err)
	case "decide":
		note, err := p.DecideLink(v.ID, v.Accept)
		writeResult(w, map[string]string{"note": note}, err)
	case "remove":
		note, err := p.RemoveDevice(v.Address)
		writeResult(w, map[string]string{"note": note}, err)
	default:
		writeErr(w, NotFound("no such device action"))
	}
}

func (s *Server) persons(w http.ResponseWriter) (Persons, bool) {
	p, ok := s.p.(Persons)
	if !ok {
		writeErr(w, NotFound("DMs are not available here"))
	}
	return p, ok
}

func (s *Server) person(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Label string `json:"label"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	p, ok := s.persons(w)
	if !ok {
		return
	}
	me, note, err := p.CreatePerson(v.Label)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"person": me, "note": note})
}

func (s *Server) dm(w http.ResponseWriter, r *http.Request) {
	p, ok := s.persons(w)
	if !ok {
		return
	}
	t, err := p.DM(r.URL.Query().Get("id"))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, t)
}

func (s *Server) newDM(w http.ResponseWriter, r *http.Request) {
	var v struct {
		Address string `json:"address"`
	}
	if !readJSON(w, r, &v) {
		return
	}
	p, ok := s.persons(w)
	if !ok {
		return
	}
	id, err := p.NewDM(v.Address)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"id": id})
}

func (s *Server) sendDM(w http.ResponseWriter, r *http.Request) {
	var d DMDraft
	if !readJSON(w, r, &d) || !s.filesHere(w, len(d.Files)) {
		return
	}
	p, ok := s.persons(w)
	if !ok {
		return
	}
	sent, err := p.SendDM(d)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, sent)
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var d Draft
	if !readJSON(w, r, &d) || !s.filesHere(w, len(d.Files)) {
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
