package ui

import (
	"encoding/json"
	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/envelope"
	"github.com/misunders2d/agentnet/internal/protocol"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type attemptDraftProvider struct {
	*Fixture
	got   RequestFollowupDraft
	calls int
}

func (p *attemptDraftProvider) RequestFollowup(d RequestFollowupDraft) (string, error) {
	p.got = d
	p.calls++
	return "sent", nil
}
func TestRequestFollowupAttemptPublicEndpoint(t *testing.T) {
	p := &attemptDraftProvider{Fixture: NewFixture(time.Now)}
	s := New(p, "127.0.0.1:8123", testToken)
	submit := func(body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8123/api/request/followup", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://127.0.0.1:8123")
		if auth {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	raw := `{"id":"` + protocol.NewID() + `","ref":{"id":"` + protocol.NewID() + `","fingerprint":"` + strings.Repeat("a", 64) + `"},"body":"correct","attempt":7,"files":["staged"]}`
	if w := submit(raw, false); w.Code != 401 || p.calls != 0 {
		t.Fatal(w.Code, p.calls)
	}
	if w := submit(raw, true); w.Code != 200 || p.got.Attempt != 7 || len(p.got.Files) != 1 {
		t.Fatal(w.Code, w.Body, p.got)
	}
	calls := p.calls
	for _, value := range []string{`1.5`, `"7"`, `9223372036854775808`} {
		if w := submit(strings.Replace(raw, `"attempt":7`, `"attempt":`+value, 1), true); w.Code != 400 || p.calls != calls {
			t.Fatal(w.Code, w.Body)
		}
	}
}
func TestRequestFollowupAttemptLiveMapping(t *testing.T) {
	alice, bob, _ := liveWorld(t)
	original, err := alice.SendMessage(t.Context(), client.Outgoing{To: bob.Address, Kind: envelope.KindQuestion, Body: "original"})
	if err != nil {
		t.Fatal(err)
	}
	l := NewLive(alice)
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	defer ts.Close()
	s = New(l, strings.TrimPrefix(ts.URL, "http://"), testToken)
	// An old device conversation takes no correction any more (MEL-713
	// step 1), whatever its attempt: the refusal is a conflict, not a 500.
	for _, attempt := range []int64{1, 0} {
		d := RequestFollowupDraft{ID: protocol.NewID(), Ref: envelope.Ref{ID: original.ID, Fingerprint: alice.Self().Fingerprint()}, Body: "correct", Attempt: attempt}
		raw, _ := json.Marshal(d)
		r := do(t, ts, "POST", "/api/request/followup", string(raw), post(ts))
		failure, _ := io.ReadAll(r.Body)
		if r.StatusCode != 409 || !strings.Contains(string(failure), "read only") {
			t.Fatal(attempt, r.StatusCode, string(failure))
		}
	}
}
