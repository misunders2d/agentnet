package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
)

const testToken = "0123456789abcdef0123456789abcdef"

func newTestServer(t *testing.T) (*httptest.Server, *Fixture) {
	t.Helper()
	f := NewFixture(func() time.Time { return time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC) })
	var s *Server
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.Handler().ServeHTTP(w, r)
	}))
	ts.Start()
	s = New(f, strings.TrimPrefix(ts.URL, "http://"), testToken)
	t.Cleanup(ts.Close)
	return ts, f
}

func do(t *testing.T, ts *httptest.Server, method, path, body string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func authed(ts *httptest.Server, extra map[string]string) map[string]string {
	h := map[string]string{"Cookie": cookieName + "=" + testToken}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

func post(ts *httptest.Server) map[string]string {
	return authed(ts, map[string]string{"Origin": ts.URL, "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"})
}

func TestGuardRefusesWhatItShould(t *testing.T) {
	ts, _ := newTestServer(t)
	cases := []struct {
		name, method, path, body string
		hdr                      map[string]string
		want                     int
	}{
		{"no cookie", "GET", "/api/state", "", nil, http.StatusUnauthorized},
		{"wrong cookie", "GET", "/api/state", "", map[string]string{"Cookie": cookieName + "=nope"}, http.StatusUnauthorized},
		{"token only on page", "GET", "/api/state?t=" + testToken, "", nil, http.StatusUnauthorized},
		{"rebinding host", "GET", "/api/state", "", authed(ts, map[string]string{"Host": "evil.example:80"}), http.StatusMisdirectedRequest},
		{"localhost alias", "GET", "/api/state", "", authed(ts, map[string]string{"Host": "localhost" + ts.URL[strings.LastIndex(ts.URL, ":"):]}), http.StatusMisdirectedRequest},
		{"post without origin", "POST", "/api/act", `{"id":"x","do":"accept"}`, authed(ts, map[string]string{"Content-Type": "application/json"}), http.StatusForbidden},
		{"post cross origin", "POST", "/api/act", `{"id":"x","do":"accept"}`, authed(ts, map[string]string{"Origin": "http://evil.example", "Content-Type": "application/json"}), http.StatusForbidden},
		{"post cross site fetch", "POST", "/api/act", `{"id":"x","do":"accept"}`, authed(ts, map[string]string{"Origin": ts.URL, "Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}), http.StatusForbidden},
		{"post form", "POST", "/api/act", `id=x&do=accept`, authed(ts, map[string]string{"Origin": ts.URL, "Content-Type": "application/x-www-form-urlencoded"}), http.StatusUnsupportedMediaType},
		{"unknown field", "POST", "/api/act", `{"id":"x","do":"accept","sudo":true}`, post(ts), http.StatusBadRequest},
		{"two objects", "POST", "/api/act", `{"id":"x","do":"accept"}{}`, post(ts), http.StatusBadRequest},
		{"no directory listing", "GET", "/assets/", "", authed(ts, nil), http.StatusNotFound},
		{"no other files", "GET", "/assets/index.html", "", authed(ts, nil), http.StatusNotFound},
	}
	for _, c := range cases {
		resp := do(t, ts, c.method, c.path, c.body, c.hdr)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
		if resp.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s: no CSP header", c.name)
		}
	}
}

func TestTokenBecomesCookieAndLeavesTheAddress(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := do(t, ts, "GET", "/?t="+testToken, "", nil)
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/" {
		t.Fatalf("status %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	c := resp.Cookies()
	if len(c) != 1 || c[0].Value != testToken || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", c)
	}
	if resp := do(t, ts, "GET", "/?t=wrong", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", resp.StatusCode)
	}
	page := do(t, ts, "GET", "/", "", authed(ts, nil))
	body, _ := io.ReadAll(page.Body)
	if page.StatusCode != 200 || !strings.Contains(string(body), "/assets/app.js") {
		t.Fatalf("page %d", page.StatusCode)
	}
	csp := page.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'none'", "script-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
}

// Peer text reaches the page only as JSON strings and is inserted as text:
// the page has no HTML sinks and no inline script or style.
func TestPageInsertsTextOnly(t *testing.T) {
	sinks := regexp.MustCompile(`innerHTML|outerHTML|insertAdjacentHTML|document\.write|eval\(|new Function`)
	err := fs.WalkDir(static, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := static.ReadFile(path)
		if loc := sinks.FindIndex(data); loc != nil {
			t.Errorf("%s uses %q", path, data[loc[0]:loc[1]])
		}
		if strings.HasSuffix(path, ".html") && (strings.Contains(string(data), "<script>") || strings.Contains(string(data), "style=")) {
			t.Errorf("%s has inline script or style (blocked by CSP)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSendAndDecideThroughTheAPI(t *testing.T) {
	ts, f := newTestServer(t)
	get := func(path string, v any) {
		resp := do(t, ts, "GET", path, "", authed(ts, nil))
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatal(err)
		}
	}
	var st State
	get("/api/state", &st)
	if !st.Demo || len(st.Review) != 3 {
		t.Fatalf("demo %v review %d", st.Demo, len(st.Review))
	}
	// A body that looks like markup is stored and returned as plain text.
	evil := `<img src=x onerror=alert(1)>`
	resp := do(t, ts, "POST", "/api/send", `{"peer":"bob/desk","kind":"question","body":"`+strings.ReplaceAll(evil, `"`, `\"`)+`"}`, post(ts))
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("send: %d %s", resp.StatusCode, b)
	}
	var c Conversation
	get("/api/conversation?peer=bob%2Fdesk", &c)
	last := c.Messages[len(c.Messages)-1]
	if last.Body != evil || last.Author.Label != "Sent from this window" || last.Next != "Waiting on bob/desk" {
		t.Fatalf("last %+v", last)
	}
	// Sending to a peer whose key changed is refused with a reason.
	resp = do(t, ts, "POST", "/api/send", `{"peer":"erin/lab","kind":"message","body":"hi"}`, post(ts))
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "paused") {
		t.Fatalf("paused send: %d %s", resp.StatusCode, b)
	}
	// Accepting the waiting task through the API starts the simulated responder.
	var task string
	for _, it := range st.Review {
		if it.Kind == KindTask {
			task = it.ID
		}
	}
	resp = do(t, ts, "POST", "/api/act", `{"id":"`+task+`","do":"accept"}`, post(ts))
	if resp.StatusCode != 200 {
		t.Fatalf("accept: %d", resp.StatusCode)
	}
	if _, m := f.find(task); m.State != "running" {
		t.Fatalf("task state %q", m.State)
	}
	if resp := do(t, ts, "POST", "/api/act", `{"id":"`+task+`","do":"accept"}`, post(ts)); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second accept: %d", resp.StatusCode)
	}
}

// The event stream sends only a counter, first at once and then on change.
func TestEventsCarryOnlyACounter(t *testing.T) {
	ts, f := newTestServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/events", nil)
	req.Header.Set("Cookie", cookieName+"="+testToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	r := bufio.NewReader(resp.Body)
	event := func() []string {
		var lines []string
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if l == "\n" {
				return lines
			}
			lines = append(lines, strings.TrimSuffix(l, "\n"))
		}
	}
	if got := event(); strings.Join(got, "|") != "event: change|data: 0" {
		t.Fatalf("first event %q", got)
	}
	if err := f.Simulate("arrival"); err != nil {
		t.Fatal(err)
	}
	if got := event(); strings.Join(got, "|") != "event: change|data: 1" {
		t.Fatalf("second event %q", got)
	}
}
