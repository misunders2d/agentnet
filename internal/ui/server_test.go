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

	"github.com/misunders2d/agentnet/internal/ui/static"
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
		{"no cookie", "GET", "/api/overview", "", nil, http.StatusUnauthorized},
		{"wrong cookie", "GET", "/api/overview", "", map[string]string{"Cookie": cookieName + "=nope"}, http.StatusUnauthorized},
		{"token only on page", "GET", "/api/overview?t=" + testToken, "", nil, http.StatusUnauthorized},
		{"rebinding host", "GET", "/api/overview", "", authed(ts, map[string]string{"Host": "evil.example:80"}), http.StatusMisdirectedRequest},
		{"localhost alias", "GET", "/api/overview", "", authed(ts, map[string]string{"Host": "localhost" + ts.URL[strings.LastIndex(ts.URL, ":"):]}), http.StatusMisdirectedRequest},
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
	for _, a := range []string{"app.js", "lenses.js", "app.css"} {
		if r := do(t, ts, "GET", "/assets/"+a, "", authed(ts, nil)); r.StatusCode != 200 {
			t.Errorf("asset %s: %d", a, r.StatusCode)
		}
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
	err := fs.WalkDir(static.Files, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, _ := fs.ReadFile(static.Files, path)
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
	get := func(path string, v any) int {
		resp := do(t, ts, "GET", path, "", authed(ts, nil))
		if resp.StatusCode == 200 {
			if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
				t.Fatal(err)
			}
		}
		return resp.StatusCode
	}
	var o Overview
	get("/api/overview", &o)
	if !o.Demo || len(o.Review) != 5 || len(o.Threads) == 0 {
		t.Fatalf("demo %v review %d threads %d", o.Demo, len(o.Review), len(o.Threads))
	}
	if code := get("/api/thread?id=nope", &Thread{}); code != http.StatusNotFound {
		t.Fatalf("unknown thread: %d", code)
	}
	// A body that looks like markup is stored and returned as plain text,
	// in a new thread of its own.
	evil := `<img src=x onerror=alert(1)>`
	resp := do(t, ts, "POST", "/api/send", `{"to":"bob/desk","kind":"question","body":"`+strings.ReplaceAll(evil, `"`, `\"`)+`"}`, post(ts))
	var sent Sent
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&sent) != nil {
		t.Fatalf("send: %d", resp.StatusCode)
	}
	var th Thread
	get("/api/thread?id="+sent.ID, &th)
	if len(th.Messages) != 1 || th.Messages[0].Body != evil || th.Messages[0].Author.Label != "This computer" ||
		th.Messages[0].Next != "Waiting on bob/desk" {
		t.Fatalf("new thread %+v", th)
	}
	// Sending to a peer whose key changed is refused with a reason.
	resp = do(t, ts, "POST", "/api/send", `{"to":"erin/lab","kind":"message","body":"hi"}`, post(ts))
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "key changed") {
		t.Fatalf("send to changed key: %d %s", resp.StatusCode, b)
	}
	// Accepting the waiting task through the API starts the simulated
	// responder, once.
	var task string
	for _, it := range o.Review {
		if it.Kind == KindTask {
			task = it.ID
		}
	}
	resp = do(t, ts, "POST", "/api/act", `{"id":"`+task+`","do":"accept"}`, post(ts))
	if b, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || !strings.Contains(string(b), `"note"`) {
		t.Fatalf("accept: %d %s", resp.StatusCode, b)
	}
	if _, m := f.find(task); m.State != "running" {
		t.Fatalf("task state %q", m.State)
	}
	if resp := do(t, ts, "POST", "/api/act", `{"id":"`+task+`","do":"accept"}`, post(ts)); resp.StatusCode != http.StatusConflict {
		t.Fatalf("second accept: %d", resp.StatusCode)
	}
	// Presence is asked for once per opened thread, and says when.
	resp = do(t, ts, "POST", "/api/refresh", `{"id":"`+task+`"}`, post(ts))
	var p Presence
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&p) != nil || p.Text != "Their computer is connected" || p.At.IsZero() {
		t.Fatalf("refresh: %d %+v", resp.StatusCode, p)
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

// A desktop alert's click opens the page without its token; with the
// session gone, the page says how to get in again. Alert controls exist
// only where the provider has alerts.
func TestAlertClickAndControls(t *testing.T) {
	ts, _ := newTestServer(t)
	resp := do(t, ts, "GET", "/", "", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(string(body), "run agentnet ui") {
		t.Fatalf("no session: %d %q", resp.StatusCode, body)
	}
	for _, what := range []string{"enable", "disable", "mute", "allow", "seen"} {
		if r := do(t, ts, "POST", "/api/notify/"+what, `{}`, post(ts)); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s on a provider without alerts: %d", what, r.StatusCode)
		}
	}
	var o map[string]any
	json.NewDecoder(do(t, ts, "GET", "/api/overview", "", authed(ts, nil)).Body).Decode(&o)
	if _, ok := o["notify"]; ok {
		t.Fatal("the overview offers alerts it does not have")
	}
}

// Reminders exist only where the provider has them.
func TestRemindersAbsentWithoutProvider(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, p := range []string{"/api/remind", "/api/remind/done", "/api/remind/cancel"} {
		if r := do(t, ts, "POST", p, `{"id":"x"}`, post(ts)); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", p, r.StatusCode)
		}
	}
	var o map[string]any
	json.NewDecoder(do(t, ts, "GET", "/api/overview", "", authed(ts, nil)).Body).Decode(&o)
	if o["remind"] != false {
		t.Fatalf("the overview offers reminders it does not have: %v", o["remind"])
	}
}

// filesFixture is the demo provider with files: what the handlers do with
// uploads and downloads, without a daemon.
type filesFixture struct {
	*Fixture
	staged    map[string][]byte
	discarded []string
}

func (f *filesFixture) DiscardFiles(ids []string) { f.discarded = append(f.discarded, ids...) }

func (f *filesFixture) StageFile(name string, r io.Reader) (string, error) {
	b, _ := io.ReadAll(r)
	f.staged[name] = b
	return "up-1", nil
}

func (f *filesFixture) OpenFile(ctx context.Context, id string, i int) (io.ReadCloser, string, error) {
	if id != "m1" || i != 0 {
		return nil, "", Refuse("received message has no such file")
	}
	return io.NopCloser(strings.NewReader("<html><script>alert(1)</script>")), `evil "name".html`, nil
}

// Files cross the page's guard only as bytes to /api/upload (with a
// name), and come back only as downloads: octet-stream, attachment,
// nosniff; pictures shown from blob: URLs are allowed by the CSP.
func TestFileRoutes(t *testing.T) {
	f := &filesFixture{Fixture: NewFixture(func() time.Time { return time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC) }), staged: map[string][]byte{}}
	var s *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	s = New(f, strings.TrimPrefix(ts.URL, "http://"), testToken)
	raw := authed(ts, map[string]string{"Origin": ts.URL, "Content-Type": "application/octet-stream", "Sec-Fetch-Site": "same-origin"})
	if r := do(t, ts, "POST", "/api/upload?name=a.png", "\x89PNG", raw); r.StatusCode != 200 || string(f.staged["a.png"]) != "\x89PNG" {
		t.Fatalf("upload: %d %v", r.StatusCode, f.staged)
	}
	for name, c := range map[string]struct {
		path string
		hdr  map[string]string
		want int
	}{
		"bytes to another route":   {"/api/dm/send", raw, http.StatusUnsupportedMediaType},
		"bytes to discard":         {"/api/upload/discard", raw, http.StatusUnsupportedMediaType},
		"an upload without name":   {"/api/upload", raw, http.StatusConflict},
		"a control in the name":    {"/api/upload?name=a%0Ab", raw, http.StatusConflict},
		"an upload cross-site":     {"/api/upload?name=a", authed(ts, map[string]string{"Origin": "http://evil.example", "Content-Type": "application/octet-stream"}), http.StatusForbidden},
		"an upload with no cookie": {"/api/upload?name=a", map[string]string{"Origin": ts.URL, "Content-Type": "application/octet-stream"}, http.StatusUnauthorized},
	} {
		if r := do(t, ts, "POST", c.path, "x", c.hdr); r.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", name, r.StatusCode, c.want)
		}
	}
	// A file removed from a draft is discarded by id; a flood is refused.
	js := authed(ts, map[string]string{"Origin": ts.URL, "Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"})
	if r := do(t, ts, "POST", "/api/upload/discard", `{"ids":["up-1"]}`, js); r.StatusCode != 200 || strings.Join(f.discarded, ",") != "up-1" {
		t.Fatalf("discard: %d %v", r.StatusCode, f.discarded)
	}
	if r := do(t, ts, "POST", "/api/upload/discard", `{"ids":[`+strings.Repeat(`"x",`, 64)+`"x"]}`, js); r.StatusCode != http.StatusConflict || len(f.discarded) != 1 {
		t.Fatalf("a discard of 65 ids: %d %v", r.StatusCode, len(f.discarded))
	}
	r := do(t, ts, "GET", "/api/files/m1/0", "", authed(ts, nil))
	body, _ := io.ReadAll(r.Body)
	h := r.Header
	if r.StatusCode != 200 || h.Get("Content-Type") != "application/octet-stream" || h.Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(h.Get("Content-Disposition"), "attachment;") || !strings.Contains(h.Get("Content-Security-Policy"), "img-src 'self' blob:") ||
		!strings.Contains(string(body), "<html>") {
		t.Fatalf("download: %d %v %q", r.StatusCode, h, body)
	}
	if r := do(t, ts, "GET", "/api/files/m1/9", "", authed(ts, nil)); r.StatusCode == 200 {
		t.Fatal("a file that is not there was served")
	}
	var o map[string]any
	json.NewDecoder(do(t, ts, "GET", "/api/overview", "", authed(ts, nil)).Body).Decode(&o)
	if _, ok := o["files"]; ok {
		t.Fatal("the demo overview offers files")
	}
	// Where files cannot be sent, a send naming some is refused, never sent without them.
	demo, _ := newTestServer(t)
	for _, c := range []struct{ path, body string }{
		{"/api/send", `{"to":"alice/desk","kind":"message","body":"x","files":["up-1"]}`},
		{"/api/dm/send", `{"conv":"d1","body":"x","files":["up-1"]}`},
	} {
		if r := do(t, demo, "POST", c.path, c.body, post(demo)); r.StatusCode != http.StatusNotFound {
			t.Errorf("%s with files in the demo: %d", c.path, r.StatusCode)
		}
	}
}
