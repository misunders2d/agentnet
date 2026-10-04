package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func typingHookRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://"+s.host+path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://"+s.host)
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
	}
	return w
}

// Subscribe before checking state, so a real stream/expiry change cannot be
// missed. Neither the product nor this fixture polls for typing.
func waitTypingHook(t *testing.T, a *client.Agent, what string, ready func() bool) {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	for {
		_, changed := a.Changed()
		if ready() {
			return
		}
		select {
		case <-changed:
		case <-timer.C:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestTypingHooksSignedTLSStreamToGuardedUI(t *testing.T) {
	alice, bob, bobLive, _ := liveWorldHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	for _, a := range []*client.Agent{alice, bob} {
		if _, err := a.CreatePerson(ctx, "Person of "+a.Address); err != nil {
			t.Fatal(err)
		}
	}
	run, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- alice.Run(run, client.RunOptions{}) }()
	stopped := false
	stopAlice := func() {
		if !stopped {
			stop()
			if err := <-done; err != nil {
				t.Errorf("alice daemon: %v", err)
			}
			stopped = true
		}
	}
	t.Cleanup(stopAlice)
	aliceLive := NewLive(alice)
	for _, l := range []*Live{aliceLive, bobLive} {
		waitTypingHook(t, l.a, "the admitted stream's typing feature", func() bool {
			v, err := l.Typing(protocol.TypingScope{})
			if err != nil {
				t.Fatal(err)
			}
			return v.Current && v.Supported
		})
	}
	sent, err := alice.Send(ctx, bob.Address, "synthetic known thread", "")
	if err != nil {
		t.Fatal(err)
	}
	waitTypingHook(t, bob, "the actual thread and pinned sender", func() bool {
		threads, err := bob.Threads()
		if err != nil {
			t.Fatal(err)
		}
		return len(threads) == 1 && threads[0].ID == sent.ID
	})
	sender := New(aliceLive, "127.0.0.1:8123", testToken)
	receiver := New(bobLive, "127.0.0.1:8124", testToken)
	query := url.Values{"peer": {alice.Address}, "thread": {sent.ID}}
	view := func() client.TypingView {
		w := typingHookRequest(t, receiver, http.MethodGet, "/api/typing?"+query.Encode(), "")
		var v client.TypingView
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	send := func(active bool) client.TypingResult {
		body, err := json.Marshal(struct {
			Scope  protocol.TypingScope `json:"scope"`
			Active bool                 `json:"active"`
		}{protocol.TypingScope{Peer: bob.Address, Thread: sent.ID}, active})
		if err != nil {
			t.Fatal(err)
		}
		w := typingHookRequest(t, sender, http.MethodPost, "/api/typing", string(body))
		var result client.TypingResult
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	// The signal is signed (in whole ms), and the throttle starts, no
	// earlier than first: "still shown" and "still throttled" below hold
	// only while the clock is inside the signed TTL (less that ms) and the
	// throttle; a loaded runner may pass them.
	first := time.Now()
	if result := send(true); result.Submitted != 1 || result.Throttled {
		t.Fatalf("live signed submission: %+v", result)
	}
	waitTypingHook(t, bob, "decrypted typing through the real UI route", func() bool { return len(view().Entries) == 1 })
	v := view()
	if len(v.Entries) != 1 && time.Since(first) < protocol.SignalTTL-time.Millisecond {
		t.Fatalf("typing gone before its signed expiry: %+v", v)
	}
	if len(v.Entries) == 1 && v.Entries[0].Address != alice.Address || v.Scope.Thread != sent.ID || !v.Current || !v.Supported {
		t.Fatalf("wrong typing view: %+v", v)
	}
	wrong := url.Values{"peer": {alice.Address}, "thread": {protocol.NewID()}}
	w := typingHookRequest(t, receiver, http.MethodGet, "/api/typing?"+wrong.Encode(), "")
	var other client.TypingView
	if err := json.Unmarshal(w.Body.Bytes(), &other); err != nil || len(other.Entries) != 0 {
		t.Fatalf("typing crossed thread scope: %+v %v", other, err)
	}
	if result := send(true); (!result.Throttled || result.Submitted != 0) && time.Since(first) < protocol.TypingThrottle {
		t.Fatalf("human throttle: %+v", result)
	}
	if result := send(false); result.Submitted != 1 || result.Throttled {
		t.Fatalf("clear must bypass human throttle: %+v", result)
	}
	waitTypingHook(t, bob, "the clear signal", func() bool { return len(view().Entries) == 0 })
	if result := send(true); result.Submitted != 1 {
		t.Fatalf("restart composer: %+v", result)
	}
	waitTypingHook(t, bob, "the new active signal", func() bool { return len(view().Entries) == 1 })
	waitTypingHook(t, bob, "local signed-TTL expiry without further traffic", func() bool { return len(view().Entries) == 0 })
	for _, a := range []*client.Agent{alice, bob} {
		threads, err := a.Threads()
		if err != nil || len(threads) != 1 || threads[0].Count != 1 {
			t.Fatalf("typing added history: %+v %v", threads, err)
		}
	}
	typingHookRequest(t, receiver, http.MethodPost, "/api/typing/preferences", `{"send":false,"show":false}`)
	if v := view(); v.Preferences.Send || v.Preferences.Show || len(v.Entries) != 0 {
		t.Fatalf("guarded local privacy settings: %+v", v)
	}
	stopAlice()
	if result := send(true); result.Submitted != 0 || result.Skipped != 0 || result.Throttled {
		t.Fatalf("disconnected composer attempted a signal: %+v", result)
	}
	if v, err := aliceLive.Typing(protocol.TypingScope{}); err != nil || v.Current {
		t.Fatalf("ended stream remained current: %+v %v", v, err)
	}
}
