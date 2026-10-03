package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type setupFixture struct {
	*Fixture
	calls int
}

func (f *setupFixture) AssistantSetup(_ context.Context, r AssistantSetupRequest) (AssistantSetupView, error) {
	f.calls++
	return AssistantSetupView{Local: true, Harnesses: []AssistantSetupHarness{}, Note: r.Action}, nil
}
func TestAssistantSetupGuardAndStrictActions(t *testing.T) {
	f := &setupFixture{Fixture: NewFixture(time.Now)}
	s := New(f, "127.0.0.1:12345", testToken)
	call := func(method, body, origin, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://127.0.0.1:12345/api/assistant-setup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		if cookie != "" {
			req.Header.Set("Cookie", cookieName+"="+cookie)
		}
		res := httptest.NewRecorder()
		s.Handler().ServeHTTP(res, req)
		return res
	}
	if r := call("GET", "", "", ""); r.Code != http.StatusUnauthorized {
		t.Fatal(r.Code)
	}
	if r := call("POST", `{"action":"apply"}`, "https://foreign.example", testToken); r.Code != http.StatusForbidden {
		t.Fatal(r.Code)
	}
	for _, body := range []string{`{"action":"delete"}`, `{"action":"apply","file":"/arbitrary/path"}`, `invalid`} {
		if r := call("POST", body, "http://127.0.0.1:12345", testToken); r.Code == 200 {
			t.Fatal("invalid setup accepted")
		}
	}
	if f.calls != 0 {
		t.Fatal("invalid requests reached installer")
	}
	if r := call("POST", `{"action":"review","harnesses":["pi"]}`, "http://127.0.0.1:12345", testToken); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if f.calls != 1 {
		t.Fatal("review not routed")
	}
}
func TestAssistantSetupAbsentProviderIsHonest(t *testing.T) {
	s := New(NewFixture(time.Now), "127.0.0.1:12345", testToken)
	r := httptest.NewRequest("GET", "http://127.0.0.1:12345/api/assistant-setup", nil)
	r.Header.Set("Cookie", cookieName+"="+testToken)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"local":false`) || !strings.Contains(w.Body.String(), "cannot inspect") {
		t.Fatal(w.Code, w.Body.String())
	}
}
