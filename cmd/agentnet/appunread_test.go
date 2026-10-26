package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/secfile"
)

type unreadTestOutput struct{ events chan appEvent }

func (o *unreadTestOutput) Write(data []byte) (int, error) {
	var event appEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return 0, err
	}
	o.events <- event
	return len(data), nil
}
func (o *unreadTestOutput) next(t *testing.T, total uint64, partial bool) appEvent {
	t.Helper()
	for {
		select {
		case event := <-o.events:
			if event.Event == "unread" && event.Total != nil && event.Partial != nil && *event.Total == total && *event.Partial == partial {
				return event
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("missing unread total=%d partial=%t", total, partial)
		}
	}
}

type unreadTestHost struct {
	mu             sync.Mutex
	totals         map[string]*uint64
	status         map[string]int
	hits           map[string]int
	messages       chan string
	requests       chan string
	closed         chan struct{}
	blockKey       string
	block, started chan struct{}
}

func (h *unreadTestHost) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("agentnet_ui")
	if err != nil || cookie.Value != "private" || r.URL.RawQuery != "" {
		http.Error(w, "no local cookie", 401)
		return
	}
	if r.URL.Path == "/api/get-app" {
		json.NewEncoder(w).Encode(map[string]string{"version": "dev"})
		return
	}
	if r.URL.Path == "/api/workspaces/events" {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		defer func() {
			select {
			case h.closed <- struct{}{}:
			default:
			}
		}()
		for {
			select {
			case <-r.Context().Done():
				return
			case message := <-h.messages:
				fmt.Fprint(w, message)
				w.(http.Flusher).Flush()
			}
		}
	}
	key := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/workspaces/"), "/api/overview")
	h.mu.Lock()
	h.hits[key]++
	total, status := h.totals[key], h.status[key]
	blocked := key == h.blockKey
	h.mu.Unlock()
	select {
	case h.requests <- key:
	default:
	}
	if blocked {
		select {
		case h.started <- struct{}{}:
		default:
		}
		select {
		case <-h.block:
		case <-r.Context().Done():
			return
		}
	}
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if total == nil {
		fmt.Fprint(w, `{"seq":0}`)
	} else {
		json.NewEncoder(w).Encode(map[string]any{"alert_total": *total, "seq": 0})
	}
}
func unreadCount(v uint64) *uint64 { return &v }
func newUnreadTestHost() *unreadTestHost {
	return &unreadTestHost{totals: map[string]*uint64{}, status: map[string]int{}, hits: map[string]int{}, messages: make(chan string, 30), requests: make(chan string, 30), closed: make(chan struct{}, 4), block: make(chan struct{}), started: make(chan struct{}, 1)}
}
func unreadMembership(rev uint64, bindings ...appUnreadBinding) string {
	if bindings == nil {
		bindings = []appUnreadBinding{}
	}
	data, _ := json.Marshal(map[string]any{"rev": rev, "bindings": bindings})
	return "event: workspaces\ndata: " + string(data) + "\n\n"
}
func unreadChange(binding appUnreadBinding) string {
	data, _ := json.Marshal(binding)
	return "event: change\ndata: " + string(data) + "\n\n"
}
func (h *unreadTestHost) set(key string, total *uint64, status int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.totals[key], h.status[key] = total, status
}
func (h *unreadTestHost) requested(t *testing.T, key string) {
	t.Helper()
	for {
		select {
		case got := <-h.requests:
			if got == key {
				return
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no scoped overview request", key)
		}
	}
}

func TestAppUnreadOwnedAttachedProjectionAndUnknown(t *testing.T) {
	for _, mode := range []string{"daemon", "attached"} {
		t.Run(mode, func(t *testing.T) {
			host := newUnreadTestHost()
			server := httptest.NewServer(host)
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			output := &unreadTestOutput{events: make(chan appEvent, 100)}
			r := &appRunner{home: t.TempDir(), out: output, unreadContext: ctx, addr: strings.TrimPrefix(server.URL, "http://"), token: "private"}
			defer r.stopUnread()
			a := appUnreadBinding{ID: "default", Handle: strings.Repeat("a", 32)}
			b := appUnreadBinding{ID: strings.Repeat("b", 32), Handle: strings.Repeat("c", 32)}
			ka, kb := a.ID+"/"+a.Handle, b.ID+"/"+b.Handle
			host.set(ka, unreadCount(2), 0)
			host.set(kb, unreadCount(3), 0)
			if mode == "attached" {
				if err := secfile.Write(filepath.Join(r.home, uiURLFile), []byte(server.URL+"/?t=private")); err != nil {
					t.Fatal(err)
				}
				r.showAttached(ctx)
			} else {
				r.emit(appEvent{Event: "page", Mode: mode, URL: r.pageURL()})
			}
			initial := output.next(t, 0, true)
			host.messages <- unreadMembership(1, a, b)
			sum := output.next(t, 5, false)
			if sum.Seq <= initial.Seq {
				t.Fatal("sequence did not advance")
			}
			host.mu.Lock()
			if host.hits[ka] != 1 || host.hits[kb] != 1 {
				t.Fatal("initial seq0 was not fetched", host.hits)
			}
			host.mu.Unlock()
			// A repeated content revision causes no extra GET or sidecar line.
			host.messages <- unreadChange(a)
			host.messages <- unreadMembership(2, a, b)
			a.Seq = 1
			host.set(ka, unreadCount(0), 0)
			host.messages <- unreadChange(a)
			zeroA := output.next(t, 3, false)
			if zeroA.Seq != sum.Seq+1 {
				t.Fatal("duplicate pair produced extra publication", sum.Seq, zeroA.Seq)
			}
			// An absent scalar is unknown, preserving only that binding's last known3.
			b.Seq = 1
			host.set(kb, nil, 0)
			host.messages <- unreadChange(b)
			unknown := output.next(t, 3, true)
			if unknown.Seq <= zeroA.Seq {
				t.Fatal("unknown transition lost")
			}
			b.Seq = 2
			host.set(kb, unreadCount(0), 0)
			host.messages <- unreadChange(b)
			zero := output.next(t, 0, false)
			raw, _ := json.Marshal(zero)
			if !strings.Contains(string(raw), `"total":0`) || !strings.Contains(string(raw), `"partial":false`) {
				t.Fatal("known zero was omitted", string(raw))
			}
			// New mount never known is omitted, with partial=true; removal clears it.
			c := appUnreadBinding{ID: strings.Repeat("d", 32), Handle: strings.Repeat("e", 32)}
			host.messages <- unreadMembership(3, a, b, c)
			output.next(t, 0, true)
			host.requested(t, c.ID+"/"+c.Handle)
			host.messages <- unreadMembership(4, a, b)
			output.next(t, 0, false)
			// A stale scoped handle contributes nothing and awaits a membership refresh.
			a.Seq = 2
			host.set(ka, nil, 409)
			host.messages <- unreadChange(a)
			host.requested(t, ka)
			b.Seq = 3
			host.set(kb, unreadCount(1), 0)
			host.messages <- unreadChange(b)
			output.next(t, 1, false)
			r.stopUnread()
			output.next(t, 0, true)
			select {
			case <-host.closed:
			case <-time.After(time.Second):
				t.Fatal("quit leaked local event handler")
			}
		})
	}
}

func TestAppUnreadRemovedAndNewPageFenceDelayedGET(t *testing.T) {
	host := newUnreadTestHost()
	server := httptest.NewServer(host)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := &unreadTestOutput{events: make(chan appEvent, 100)}
	r := &appRunner{out: output, unreadContext: ctx}
	defer r.stopUnread()
	a := appUnreadBinding{ID: "default", Handle: strings.Repeat("a", 32)}
	key := a.ID + "/" + a.Handle
	host.set(key, unreadCount(99), 0)
	host.blockKey = key
	r.emit(appEvent{Event: "page", Mode: "daemon", URL: server.URL + "/?t=private"})
	output.next(t, 0, true)
	host.messages <- unreadMembership(1, a)
	select {
	case <-host.started:
	case <-time.After(time.Second):
		t.Fatal("GET did not start")
	}
	host.messages <- unreadMembership(2)
	output.next(t, 0, false)
	close(host.block)
	// A subsequent admitted mount/result is a deterministic ordering barrier.
	b := appUnreadBinding{ID: strings.Repeat("b", 32), Handle: strings.Repeat("c", 32)}
	host.set(b.ID+"/"+b.Handle, unreadCount(2), 0)
	host.messages <- unreadMembership(3, b)
	output.next(t, 2, false)
	r.outMu.Lock()
	oldEpoch := r.unread
	r.outMu.Unlock()
	r.emit(appEvent{Event: "page", Mode: "setup", URL: server.URL + "/?t=new"})
	r.publishUnread(oldEpoch, 99, false)
	output.next(t, 0, true)
	r.stopUnread()
	for {
		select {
		case e := <-output.events:
			if e.Event == "unread" && e.Total != nil && *e.Total == 99 {
				t.Fatal("removed/delayed response revived")
			}
		default:
			return
		}
	}
}

func TestAppUnreadReconnectWindowAndCookieRedirect(t *testing.T) {
	output := &unreadTestOutput{events: make(chan appEvent, 20)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &appUnreadSession{ctx: ctx, cancel: cancel}
	r := &appRunner{out: output, unread: session}
	r.publishUnread(session, 7, false)
	output.next(t, 7, false)
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "temporarily unavailable", 503) }))
	defer failed.Close()
	u, _ := url.Parse(failed.URL + "/api/workspaces/events")
	done := make(chan struct{})
	go func() { r.consumeUnreadPage(session, failed.Client(), u, "private", 30*time.Millisecond); close(done) }()
	output.next(t, 0, true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("bounded reconnect did not stop")
	}
	var leaked atomic.Int32
	victim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { leaked.Add(1); w.WriteHeader(204) }))
	defer victim.Close()
	attempted := make(chan struct{}, 1)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		select {
		case attempted <- struct{}{}:
		default:
		}
		http.Redirect(w, req, victim.URL, 302)
	}))
	defer redirect.Close()
	redirected := &appRunner{out: output, unreadContext: ctx}
	redirected.emit(appEvent{Event: "page", Mode: "daemon", URL: redirect.URL + "/?t=private"})
	select {
	case <-attempted:
	case <-time.After(time.Second):
		t.Fatal("local connection not attempted")
	}
	redirected.stopUnread()
	if leaked.Load() != 0 {
		t.Fatal("local cookie followed redirect")
	}

	for _, bad := range []string{"https://127.0.0.1:44/?t=p", "http://example.com:44/?t=p", "http://user@127.0.0.1:44/?t=p", "http://127.0.0.1:44/"} {
		if _, _, err := appUnreadEndpoint(bad); err == nil {
			t.Fatal("unsafe binding accepted")
		}
	}
}

func TestAppUnreadReconnectRevalidatesZeroRevisionAndHoldsPending(t *testing.T) {
	output := &unreadTestOutput{events: make(chan appEvent, 30)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &appUnreadSession{ctx: ctx, cancel: cancel}
	runner := &appRunner{out: output, unread: session, unreadContext: ctx}
	binding := appUnreadBinding{ID: "default", Handle: strings.Repeat("a", 32)}
	disconnect := make(chan struct{})
	secondGET := make(chan struct{})
	releaseGET := make(chan struct{})
	var streams, gets atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("agentnet_ui")
		if err != nil || cookie.Value != "private" {
			http.Error(w, "cookie", 401)
			return
		}
		if r.URL.Path == "/api/workspaces/events" {
			connection := streams.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, unreadMembership(1, binding))
			w.(http.Flusher).Flush()
			if connection == 1 {
				select {
				case <-disconnect:
				case <-r.Context().Done():
				}
			} else {
				<-r.Context().Done()
			}
			return
		}
		n := gets.Add(1)
		if n == 2 {
			close(secondGET)
			select {
			case <-releaseGET:
			case <-r.Context().Done():
				return
			}
		}
		total := uint64(2)
		if n > 1 {
			total = 7
		}
		json.NewEncoder(w).Encode(map[string]uint64{"alert_total": total})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL + "/api/workspaces/events")
	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.consumeUnreadPage(session, server.Client(), u, "private", time.Second)
	}()
	first := output.next(t, 2, false)
	close(disconnect)
	select {
	case <-secondGET:
	case <-time.After(2 * time.Second):
		t.Fatal("new stream did not refetch seq0")
	}
	select {
	case e := <-output.events:
		t.Fatalf("reconnect cleared last value before fresh GET: %+v", e)
	default:
	}
	close(releaseGET)
	second := output.next(t, 7, false)
	if second.Seq <= first.Seq || streams.Load() != 2 || gets.Load() != 2 {
		t.Fatal("reconnect epoch/dedup", first, second, streams.Load(), gets.Load())
	}
	runner.emit(appEvent{Event: "error", Text: "synthetic daemon error"})
	runner.stopUnread()
	output.next(t, 0, true)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reconnected handler did not stop")
	}
}
