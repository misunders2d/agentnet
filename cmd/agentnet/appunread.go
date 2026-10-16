package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Native indicators consume the admitted local projection, never an Agent store
// or a second Hub stream. A page session owns every HTTP operation and callback.
type appUnreadSession struct {
	ctx              context.Context
	cancel           context.CancelFunc
	seq              uint64
	total            uint64
	partial, emitted bool // accessed only under appRunner.outMu
}

type appUnreadBinding struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
	Seq    uint64 `json:"seq"`
}
type appUnreadValue struct {
	binding                 appUnreadBinding
	total                   uint64
	known, unknown, pending bool
}
type appUnreadFetch struct {
	binding appUnreadBinding
	total   *uint64
	stale   bool
}
type appUnreadMessage struct {
	name string
	data []byte
	err  error
}

func appUnreadIdentity(id, handle string) bool {
	hex := func(value string) bool {
		if len(value) != 32 {
			return false
		}
		for _, r := range value {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
		return true
	}
	return (id == "default" || hex(id)) && hex(handle)
}
func appUnreadKey(b appUnreadBinding) string { return b.ID + ":" + b.Handle }

// Called with outMu held, after the page/error line itself. Cancellation and
// publication share this lock: no old page response can follow a new page.
func (r *appRunner) unreadPageLocked(e appEvent) {
	previous := r.unread
	if previous != nil {
		previous.cancel()
		r.unread = nil
	}
	if r.unreadContext == nil {
		return
	}
	if e.Event == "page" && (e.Mode == "daemon" || e.Mode == "attached") {
		ctx, cancel := context.WithCancel(r.unreadContext)
		session := &appUnreadSession{ctx: ctx, cancel: cancel}
		r.unread = session
		r.writeUnreadLocked(session, 0, true)
		r.unreadWG.Add(1)
		go func() { defer r.unreadWG.Done(); r.watchUnread(session, e.URL) }()
	} else if previous != nil {
		r.writeUnreadLocked(previous, 0, true)
	}
}
func (r *appRunner) writeUnreadLocked(session *appUnreadSession, total uint64, partial bool) {
	if session.emitted && session.total == total && session.partial == partial {
		return
	}
	session.seq++
	session.total, session.partial, session.emitted = total, partial, true
	r.writeEventLocked(appEvent{Event: "unread", Workspace: "*", Seq: session.seq, Total: &total, Partial: &partial})
}
func (r *appRunner) publishUnread(session *appUnreadSession, total uint64, partial bool) {
	r.outMu.Lock()
	defer r.outMu.Unlock()
	if r.unread != session || session.ctx.Err() != nil {
		return
	}
	r.writeUnreadLocked(session, total, partial)
}
func (r *appRunner) stopUnread() {
	r.outMu.Lock()
	r.unreadContext = nil
	if r.unread != nil {
		session := r.unread
		session.cancel()
		r.writeUnreadLocked(session, 0, true)
		r.unread = nil
	}
	r.outMu.Unlock()
	r.unreadWG.Wait()
}

func appUnreadEndpoint(endpoint string) (*url.URL, string, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Port() == "" || !net.ParseIP(u.Hostname()).IsLoopback() || u.Query().Get("t") == "" {
		return nil, "", errors.New("unavailable local page binding")
	}
	token := u.Query().Get("t")
	u.Path, u.RawPath, u.RawQuery, u.Fragment = "/api/workspaces/events", "", "", ""
	return u, token, nil
}
func appUnreadRequest(ctx context.Context, c *http.Client, u *url.URL, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: token})
	return c.Do(req)
}
func (r *appRunner) watchUnread(session *appUnreadSession, endpoint string) {
	u, token, err := appUnreadEndpoint(endpoint)
	if err != nil {
		return
	} // the initial display-clear is unknown, never a zero claim
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = 5 * time.Second
	defer transport.CloseIdleConnections()
	c := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r.consumeUnreadPage(session, c, u, token, appStartTimeout)
}

// The last displayed value survives a transient stream failure for one bounded
// restart window. After expiry clear the display (partial/unknown), then stop
// until the runner announces a ready page again. This never marks anything read.
func (r *appRunner) consumeUnreadPage(session *appUnreadSession, c *http.Client, u *url.URL, token string, window time.Duration) {
	deadline := time.Now().Add(window)
	delay := 100 * time.Millisecond
	for session.ctx.Err() == nil {
		connected := r.unreadConnection(session, c, u, token)
		if session.ctx.Err() != nil {
			return
		}
		if connected {
			deadline = time.Now().Add(window)
			delay = 100 * time.Millisecond
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			r.publishUnread(session, 0, true)
			return
		}
		pause := min(delay, remaining)
		timer := time.NewTimer(pause)
		select {
		case <-session.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if time.Now().After(deadline) {
			r.publishUnread(session, 0, true)
			return
		}
		delay = min(delay*2, 2*time.Second)
	}
}

func scanAppUnread(ctx context.Context, body io.Reader, messages chan<- appUnreadMessage) {
	send := func(message appUnreadMessage) bool {
		select {
		case messages <- message:
			return true
		case <-ctx.Done():
			return false
		}
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	name := ""
	var data strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if (name == "workspaces" || name == "change") && data.Len() > 0 {
				if !send(appUnreadMessage{name: name, data: []byte(data.String())}) {
					return
				}
			}
			name = ""
			data.Reset()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len()+len(line) > 4<<20 {
				send(appUnreadMessage{err: errors.New("local invalidation frame exceeded bound")})
				return
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	send(appUnreadMessage{err: err})
}

func fetchAppUnread(ctx context.Context, c *http.Client, base *url.URL, token string, fetch appUnreadFetch) appUnreadFetch {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u := *base
	u.Path = "/workspaces/" + fetch.binding.ID + "/" + fetch.binding.Handle + "/api/overview"
	res, err := appUnreadRequest(requestCtx, c, &u, token)
	if err != nil {
		return fetch
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusConflict {
		fetch.stale = true
		return fetch
	}
	if res.StatusCode != http.StatusOK {
		return fetch
	}
	var value struct {
		Total *uint64 `json:"alert_total"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, (4<<20)+1))
	if err == nil && len(data) <= 4<<20 && json.Unmarshal(data, &value) == nil {
		fetch.total = value.Total
	}
	return fetch
}

// Each connection is an epoch. One decoder and one bounded fetch worker permit
// membership/content events to invalidate an outstanding GET before it returns.
// No worker or retained queue is created per workspace.
func (r *appRunner) unreadConnection(session *appUnreadSession, c *http.Client, u *url.URL, token string) (connected bool) {
	ctx, cancel := context.WithCancel(session.ctx)
	defer cancel()
	res, err := appUnreadRequest(ctx, c, u, token)
	if err != nil {
		return false
	}
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		res.Body.Close()
		return false
	}
	messages := make(chan appUnreadMessage, 1)
	requests := make(chan appUnreadFetch)
	results := make(chan appUnreadFetch, 1)
	readerDone, fetchDone := make(chan struct{}), make(chan struct{})
	go func() { defer close(readerDone); scanAppUnread(ctx, res.Body, messages) }()
	go func() {
		defer close(fetchDone)
		for {
			select {
			case <-ctx.Done():
				return
			case fetch := <-requests:
				value := fetchAppUnread(ctx, c, u, token, fetch)
				select {
				case results <- value:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() { cancel(); res.Body.Close(); <-readerDone; <-fetchDone }()
	states := map[string]*appUnreadValue{}
	var rev uint64
	haveRev, busy := false, false
	var inFlight appUnreadBinding
	publish := func() {
		total := uint64(0)
		partial := false
		for _, state := range states {
			partial = partial || state.unknown
			if state.known {
				if math.MaxUint64-total < state.total {
					total = math.MaxUint64
					partial = true
				} else {
					total += state.total
				}
			}
		}
		r.publishUnread(session, total, partial)
	}
	for {
		var request appUnreadFetch
		var send chan appUnreadFetch
		if !busy {
			for _, state := range states {
				if state.pending {
					request.binding = state.binding
					send = requests
					break
				}
			}
		}
		select {
		case <-ctx.Done():
			return connected
		case send <- request:
			busy = true
			inFlight = request.binding
			if state := states[appUnreadKey(request.binding)]; state != nil {
				state.pending = false
			}
		case result := <-results:
			busy = false
			key := appUnreadKey(result.binding)
			state := states[key]
			if state == nil || state.binding.Seq != result.binding.Seq {
				continue
			}
			if result.stale {
				delete(states, key)
			} else if result.total == nil {
				state.unknown = true
			} else {
				state.total, state.known, state.unknown = *result.total, true, false
			}
			publish()
		case message := <-messages:
			if message.err != nil {
				return connected
			}
			switch message.name {
			case "workspaces":
				var value struct {
					Rev      uint64              `json:"rev"`
					Bindings *[]appUnreadBinding `json:"bindings"`
				}
				if json.Unmarshal(message.data, &value) != nil || value.Bindings == nil {
					return connected
				}
				if haveRev && value.Rev <= rev {
					continue
				}
				next := make(map[string]*appUnreadValue, len(*value.Bindings))
				ids := map[string]bool{}
				for _, binding := range *value.Bindings {
					if !appUnreadIdentity(binding.ID, binding.Handle) || ids[binding.ID] {
						return connected
					}
					ids[binding.ID] = true
					key := appUnreadKey(binding)
					state := states[key]
					if state == nil {
						state = &appUnreadValue{binding: binding, unknown: true, pending: true}
					} else if binding.Seq > state.binding.Seq {
						state.binding.Seq = binding.Seq
						state.pending = true
					} else if state.unknown && !(busy && appUnreadKey(inFlight) == key && inFlight.Seq == state.binding.Seq) {
						state.pending = true
					}
					next[key] = state
				}
				initial := !haveRev
				states, rev, haveRev, connected = next, value.Rev, true, true
				// On reconnect, keep the prior display while the first fresh
				// GET is pending. Empty membership is an authoritative clear.
				if !initial || len(states) == 0 {
					publish()
				}
			case "change":
				var binding appUnreadBinding
				if json.Unmarshal(message.data, &binding) != nil || !appUnreadIdentity(binding.ID, binding.Handle) {
					return connected
				}
				state := states[appUnreadKey(binding)]
				if state != nil && binding.Seq > state.binding.Seq {
					state.binding.Seq = binding.Seq
					state.pending = true
				}
			}
		}
	}
}
