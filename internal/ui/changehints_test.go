package ui

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

type hintedFixture struct {
	Provider
	mu       sync.Mutex
	revision client.ViewChange
	next     chan struct{}
}

func (f *hintedFixture) ViewChanges() (client.ViewChange, <-chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revision, f.next
}
func (f *hintedFixture) Changed() (uint64, <-chan struct{}) {
	r, ch := f.ViewChanges()
	return r.Seq, ch
}
func (f *hintedFixture) change(kinds ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, kind := range kinds {
		f.revision.Seq++
		switch kind {
		case "typing":
			f.revision.Typing = f.revision.Seq
		case "presence":
			f.revision.Presence = f.revision.Seq
		default:
			f.revision.Content = f.revision.Seq
		}
	}
	close(f.next)
	f.next = make(chan struct{})
}

func TestChangeHintsNeverHideCoalescedDurableChanges(t *testing.T) {
	f := &hintedFixture{Provider: NewFixture(time.Now), next: make(chan struct{})}
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(f, strings.TrimPrefix(ts.URL, "http://"), testToken)
	r := do(t, ts, "GET", "/events", "", authed(ts, nil))
	scan := bufio.NewScanner(r.Body)
	read := func(want string) {
		t.Helper()
		for scan.Scan() {
			if strings.HasPrefix(scan.Text(), "event:") {
				if scan.Text() != "event: "+want {
					t.Fatalf("event %q want %s", scan.Text(), want)
				}
				return
			}
		}
		t.Fatal("stream ended")
	}
	read("change")
	f.change("typing")
	read("typing")
	f.change("presence")
	read("presence")
	f.change("message", "typing", "presence")
	read("change")
	f.change("presence", "typing")
	read("presence")
	read("typing")
	r.Body.Close()
}

type scopedFixture struct {
	*hintedFixture
	keys []string
}

func (f *scopedFixture) ViewScope(mark int64) ([]string, int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if mark < 0 {
		return nil, 0, true
	}
	return f.keys, mark + 1, true
}

// "scope" comes right before the "change" it names, which stays as it was for
// an older page; a change no key names (Unscoped) carries no scope.
func TestScopeEventPrecedesItsChange(t *testing.T) {
	f := &scopedFixture{hintedFixture: &hintedFixture{Provider: NewFixture(time.Now), next: make(chan struct{})}, keys: []string{"c:B"}}
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(f, strings.TrimPrefix(ts.URL, "http://"), testToken)
	r := do(t, ts, "GET", "/events", "", authed(ts, nil))
	defer r.Body.Close()
	scan := bufio.NewScanner(r.Body)
	next := func() string {
		t.Helper()
		var lines []string
		for scan.Scan() {
			if scan.Text() == "" {
				return strings.Join(lines, "|")
			}
			lines = append(lines, scan.Text())
		}
		t.Fatal("stream ended")
		return ""
	}
	if got := next(); got != "event: change|data: 0" {
		t.Fatalf("first: %q", got)
	}
	f.change("message")
	if got := next(); got != `event: scope|data: {"seq":1,"keys":["c:B"]}` {
		t.Fatalf("scoped: %q", got)
	}
	if got := next(); got != "event: change|data: 1" {
		t.Fatalf("its change: %q", got)
	}
	f.mu.Lock()
	f.revision.Seq++
	f.revision.Content, f.revision.Unscoped = f.revision.Seq, f.revision.Seq
	close(f.next)
	f.next = make(chan struct{})
	f.mu.Unlock()
	if got := next(); got != "event: change|data: 2" {
		t.Fatalf("unscoped: %q", got)
	}
}
