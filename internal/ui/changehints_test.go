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
