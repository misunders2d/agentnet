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

// unreadTestHost speaks only the local event stream. Any other request is a
// view the badge must never build (MEL-731).
type unreadTestHost struct {
	mu       sync.Mutex
	hits     []string
	messages chan string
	closed   chan struct{}
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
	if r.URL.Path != "/api/workspaces/events" {
		h.mu.Lock()
		h.hits = append(h.hits, r.URL.Path)
		h.mu.Unlock()
		http.NotFound(w, r)
		return
	}
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
func (h *unreadTestHost) views(t *testing.T) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.hits) != 0 {
		t.Fatal("the badge asked for a view", h.hits)
	}
}
func newUnreadTestHost() *unreadTestHost {
	return &unreadTestHost{messages: make(chan string, 30), closed: make(chan struct{}, 4)}
}
func counted(b appUnreadBinding, seq, total uint64) appUnreadBinding {
	b.Seq, b.AlertTotal = seq, &total
	return b
}
func uncounted(b appUnreadBinding, seq uint64) appUnreadBinding {
	b.Seq, b.AlertTotal = seq, nil
	return b
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
			if mode == "attached" {
				if err := secfile.Write(filepath.Join(r.home, uiURLFile), []byte(server.URL+"/?t=private")); err != nil {
					t.Fatal(err)
				}
				r.showAttached(ctx)
			} else {
				r.emit(appEvent{Event: "page", Mode: mode, URL: r.pageURL()})
			}
			initial := output.next(t, 0, true)
			// Pushed totals add up at once: no view is requested (MEL-731).
			host.messages <- unreadMembership(1, counted(a, 0, 2), counted(b, 0, 3))
			sum := output.next(t, 5, false)
			if sum.Seq <= initial.Seq {
				t.Fatal("sequence did not advance")
			}
			// A content change that keeps the total publishes nothing.
			host.messages <- unreadChange(counted(a, 1, 2))
			host.messages <- unreadChange(counted(a, 2, 0))
			zeroA := output.next(t, 3, false)
			if zeroA.Seq != sum.Seq+1 {
				t.Fatal("unchanged total produced extra publication", sum.Seq, zeroA.Seq)
			}
			// A stale change and one for no current binding are ignored.
			host.messages <- unreadChange(counted(a, 1, 40))
			host.messages <- unreadChange(counted(appUnreadBinding{ID: strings.Repeat("f", 32), Handle: strings.Repeat("f", 32)}, 9, 50))
			// A workspace not counted yet makes the sum partial.
			c := appUnreadBinding{ID: strings.Repeat("d", 32), Handle: strings.Repeat("e", 32)}
			host.messages <- unreadMembership(2, counted(a, 2, 0), counted(b, 0, 3), uncounted(c, 0))
			output.next(t, 3, true)
			host.messages <- unreadChange(counted(c, 1, 0))
			zero := output.next(t, 3, false)
			raw, _ := json.Marshal(zero)
			if !strings.Contains(string(raw), `"partial":false`) {
				t.Fatal("known total was not final", string(raw))
			}
			// Removal drops its total.
			host.messages <- unreadMembership(3, counted(a, 2, 0))
			output.next(t, 0, false)
			r.stopUnread()
			output.next(t, 0, true)
			select {
			case <-host.closed:
			case <-time.After(time.Second):
				t.Fatal("quit leaked local event handler")
			}
			for {
				select {
				case e := <-output.events:
					if e.Total != nil && (*e.Total == 40 || *e.Total == 50) {
						t.Fatal("stale or foreign change published", e)
					}
					continue
				default:
				}
				break
			}
			host.views(t)
		})
	}
}

func TestAppUnreadRemovedAndNewPageFence(t *testing.T) {
	host := newUnreadTestHost()
	server := httptest.NewServer(host)
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	output := &unreadTestOutput{events: make(chan appEvent, 100)}
	r := &appRunner{out: output, unreadContext: ctx}
	defer r.stopUnread()
	a := appUnreadBinding{ID: "default", Handle: strings.Repeat("a", 32)}
	r.emit(appEvent{Event: "page", Mode: "daemon", URL: server.URL + "/?t=private"})
	output.next(t, 0, true)
	host.messages <- unreadMembership(1, counted(a, 0, 99))
	output.next(t, 99, false)
	host.messages <- unreadMembership(2)
	output.next(t, 0, false)
	b := appUnreadBinding{ID: strings.Repeat("b", 32), Handle: strings.Repeat("c", 32)}
	host.messages <- unreadMembership(3, counted(b, 0, 2))
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
				t.Fatal("removed workspace or old page revived")
			}
		default:
			host.views(t)
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

// A restarted daemon counts again from scratch: the badge keeps showing the
// last total until the new connection's total is counted.
func TestAppUnreadReconnectHoldsLastTotalUntilCounted(t *testing.T) {
	output := &unreadTestOutput{events: make(chan appEvent, 30)}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &appUnreadSession{ctx: ctx, cancel: cancel}
	runner := &appRunner{out: output, unread: session, unreadContext: ctx}
	binding := appUnreadBinding{ID: "default", Handle: strings.Repeat("a", 32)}
	disconnect, secondStream, count := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var streams, views atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("agentnet_ui")
		if err != nil || cookie.Value != "private" {
			http.Error(w, "cookie", 401)
			return
		}
		if r.URL.Path != "/api/workspaces/events" {
			views.Add(1)
			http.NotFound(w, r)
			return
		}
		connection := streams.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if connection == 1 {
			fmt.Fprint(w, unreadMembership(1, counted(binding, 0, 2)))
			w.(http.Flusher).Flush()
			select {
			case <-disconnect:
			case <-r.Context().Done():
			}
			return
		}
		fmt.Fprint(w, unreadMembership(1, uncounted(binding, 0)))
		w.(http.Flusher).Flush()
		close(secondStream)
		select {
		case <-count:
		case <-r.Context().Done():
			return
		}
		fmt.Fprint(w, unreadChange(counted(binding, 1, 7)))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
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
	case <-secondStream:
	case <-time.After(2 * time.Second):
		t.Fatal("no new stream")
	}
	close(count)
	select {
	case e := <-output.events:
		if e.Event != "unread" || e.Total == nil || *e.Total != 7 || *e.Partial || e.Seq <= first.Seq {
			t.Fatalf("reconnect replaced the last total before it was counted: %+v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("counted total not published")
	}
	if streams.Load() != 2 || views.Load() != 0 {
		t.Fatal("reconnect", streams.Load(), views.Load())
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
