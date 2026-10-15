package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

// Exercise the real HTTP -> Live -> client path: opting into queued controls
// only in client tests would miss a UI handler that forgot WithQueuedSend.
func TestLiveHTTPControlsCommitBeforeDelivery(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	type barrier struct {
		sender                     string
		entered, release, finished chan struct{}
		once                       sync.Once
	}
	var held atomic.Pointer[barrier]
	dir, _, alice := platformHub(t, ctx, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if b := held.Load(); b != nil && r.Method == "POST" && r.URL.Path == "/v1/messages" && r.Header.Get(protocol.HeaderAgent) == b.sender {
				b.once.Do(func() { close(b.entered) })
				select {
				case <-b.release:
				case <-r.Context().Done():
					return
				}
				defer close(b.finished)
			}
			next.ServeHTTP(w, r)
		})
	})
	code, err := alice.Invite(ctx, "bob", time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := protocol.DecodeInvite(code)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := os.ReadFile(filepath.Join(dir, "tls.crt"))
	if err != nil {
		t.Fatal(err)
	}
	inv.CertPEM = string(cert)
	bob, err := client.Join(ctx, t.TempDir(), inv.Encode(), "desk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { bob.Close() })
	runDaemon(t, alice)
	runDaemon(t, bob)
	original, err := alice.Send(ctx, bob.Address, "Before Save", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "recipient received initial turn and published capabilities", func() bool { v, e := NewLive(bob).Thread(original.ID); return e == nil && len(v.Messages) > 0 })
	live := NewLive(alice)
	handler := New(live, "127.0.0.1:8123", testToken).Handler()
	for _, action := range []string{"edit", "react", "unreact", "delete"} {
		t.Run(action, func(t *testing.T) {
			b := &barrier{sender: alice.Address, entered: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
			held.Store(b)
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(b.release) }) }
			defer release()
			v := ControlAction{ID: original.ID, Dir: "out", Text: "After Save", Emoji: "👍", Remove: action == "unreact"}
			body, _ := json.Marshal(v)
			path := action
			if action == "unreact" {
				path = "react"
			}
			req := httptest.NewRequest("POST", "http://127.0.0.1:8123/api/message/"+path, strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", "http://127.0.0.1:8123")
			req.Header.Set("Cookie", cookieName+"="+testToken)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { w := httptest.NewRecorder(); handler.ServeHTTP(w, req); done <- w }()
			select {
			case w := <-done:
				if w.Code != 200 {
					t.Fatalf("HTTP %d: %s", w.Code, w.Body)
				}
				if action == "delete" && !strings.Contains(w.Body.String(), "Deletion queued for other devices") {
					t.Fatalf("unproven remote deletion note: %s", w.Body)
				}
			case <-time.After(time.Second):
				release()
				<-done
				t.Fatal("Live HTTP control waited for remote delivery")
			}
			select {
			case <-b.entered:
			case <-time.After(time.Second):
				t.Fatal("queued control did not reach delivery barrier")
			}
			thread, e := live.Thread(original.ID)
			if e != nil || len(thread.Messages) != 1 {
				t.Fatalf("local projection: %+v %v", thread, e)
			}
			m := thread.Messages[0]
			switch action {
			case "edit":
				if !m.Edited || m.Text != "After Save" {
					t.Fatalf("edit not local: %+v", m.Controls)
				}
			case "react":
				if len(m.Reactions) != 1 {
					t.Fatal("reaction not local")
				}
			case "unreact":
				if len(m.Reactions) != 0 {
					t.Fatal("reaction removal not local")
				}
			case "delete":
				if !m.Deleted {
					t.Fatal("deletion not local")
				}
			}
			release()
			select {
			case <-b.finished:
			case <-time.After(time.Second):
				t.Fatal("held delivery did not finish")
			}
			if e = alice.FlushOutbox(ctx); e != nil {
				t.Fatal(e)
			}
			held.Store(nil)
		})
	}
}
