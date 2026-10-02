package ui

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type typingFixture struct {
	*Fixture
	view   client.TypingView
	err    error
	calls  int
	scope  protocol.TypingScope
	active bool
	prefs  client.TypingPreferences
}

func (f *typingFixture) Typing(s protocol.TypingScope) (client.TypingView, error) {
	f.calls++
	f.scope = s
	return f.view, f.err
}
func (f *typingFixture) SendTyping(_ context.Context, s protocol.TypingScope, active bool) (client.TypingResult, error) {
	f.calls++
	f.scope = s
	f.active = active
	return client.TypingResult{Submitted: 1}, f.err
}
func (f *typingFixture) SetTypingPreferences(p client.TypingPreferences) error {
	f.calls++
	f.prefs = p
	return f.err
}
func TestTypingProviderScopedGuardPrivacyAndRedaction(t *testing.T) {
	f := &typingFixture{Fixture: NewFixture(time.Now), view: client.TypingView{Entries: []client.TypingEntry{}, Preferences: client.TypingPreferences{Send: true, Show: true}, Current: true, Supported: true}}
	s := New(f, "127.0.0.1:8123", testToken)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/typing", s.typingView)
	mux.HandleFunc("POST /api/typing", s.sendTyping)
	mux.HandleFunc("POST /api/typing/preferences", s.typingPreferences)
	handler := s.guard(mux)
	request := func(method, path, body, origin string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8123"+path, strings.NewReader(body))
		if auth {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		}
		if method == "POST" {
			r.Header.Set("Origin", origin)
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, path, body, origin string
		auth                       bool
		status                     int
	}{
		{"GET", "/api/typing", "", "", false, 401},
		{"POST", "/api/typing", `{"active":true}`, "http://untrusted.invalid", true, 403},
		{"POST", "/api/typing", `{"active":true,"body":"private draft"}`, "http://127.0.0.1:8123", true, 400},
		{"POST", "/api/typing/preferences", `{"send":true,"show":true,"automatic_job":true}`, "http://127.0.0.1:8123", true, 400},
	} {
		before := f.calls
		w := request(tc.method, tc.path, tc.body, tc.origin, tc.auth)
		if w.Code != tc.status || f.calls != before {
			t.Fatalf("guard: %d %s calls=%d", w.Code, w.Body, f.calls)
		}
	}
	id := protocol.NewID()
	scope := protocol.TypingScope{Peer: "alice/desk", Thread: id}
	w := request("GET", "/api/typing?peer=alice/desk&thread="+id, "", "", true)
	var view client.TypingView
	if err := json.Unmarshal(w.Body.Bytes(), &view); w.Code != 200 || err != nil || f.scope != scope || !view.Current {
		t.Fatalf("scope: %d %s %v", w.Code, w.Body, err)
	}
	body := `{"scope":{"peer":"alice/desk","thread":"` + id + `"},"active":true}`
	w = request("POST", "/api/typing", body, "http://127.0.0.1:8123", true)
	if w.Code != 200 || !f.active || f.scope != scope || !strings.Contains(w.Body.String(), "submitted") {
		t.Fatalf("submit: %d %s", w.Code, w.Body)
	}
	w = request("POST", "/api/typing/preferences", `{"send":false,"show":false}`, "http://127.0.0.1:8123", true)
	if w.Code != 200 || f.prefs.Send || f.prefs.Show {
		t.Fatalf("privacy: %d %s", w.Code, w.Body)
	}
	f.err = errors.New("token=synthetic-private /private/draft")
	w = request("POST", "/api/typing", body, "http://127.0.0.1:8123", true)
	if w.Code < 400 || strings.Contains(w.Body.String(), "synthetic-private") || strings.Contains(w.Body.String(), "/private") {
		t.Fatalf("provider leaked private failure: %d %s", w.Code, w.Body)
	}
	demo := New(NewFixture(time.Now), "127.0.0.1:8123", testToken)
	w = httptest.NewRecorder()
	demo.typingView(w, httptest.NewRequest("GET", "/api/typing", nil))
	if w.Code != 404 {
		t.Fatal("unsupported provider fabricated typing")
	}
}
func TestTypingLivePreferencesAreLocalAndExplicit(t *testing.T) {
	_, _, live, _ := liveWorldHub(t)
	v, err := live.Typing(protocol.TypingScope{})
	if err != nil || v.Preferences.Send || v.Preferences.Show || len(v.Entries) != 0 {
		t.Fatalf("unset: %+v %v", v, err)
	}
	if _, _, err := live.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	v, err = live.Typing(protocol.TypingScope{})
	if err != nil || !v.Preferences.Send || !v.Preferences.Show {
		t.Fatalf("person default: %+v %v", v, err)
	}
	if err := live.SetTypingPreferences(client.TypingPreferences{}); err != nil {
		t.Fatal(err)
	}
	v, err = live.Typing(protocol.TypingScope{})
	if err != nil || v.Preferences.Send || v.Preferences.Show {
		t.Fatalf("local opt-out: %+v %v", v, err)
	}
}
