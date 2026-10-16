package ui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
)

func TestScheduleUnsupportedAndMethods(t *testing.T) {
	ts, _ := newTestServer(t)
	for _, path := range []string{"/api/schedules", "/api/schedules?id=" + protocol.NewID(), "/api/schedule/audience?conv=" + strings.Repeat("a", 64)} {
		r := do(t, ts, "GET", path, "", authed(ts, nil))
		if r.StatusCode != 404 {
			t.Fatal(path, r.StatusCode)
		}
	}
	r := do(t, ts, "GET", "/api/overview", "", authed(ts, nil))
	var o Overview
	if err := json.NewDecoder(r.Body).Decode(&o); err != nil || o.Schedules {
		t.Fatal(o.Schedules, err)
	}
	if r = do(t, ts, "POST", "/api/schedule/create", "{}", post(ts)); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
	if r = do(t, ts, "POST", "/api/schedules", "{}", post(ts)); r.StatusCode != 405 {
		t.Fatal(r.StatusCode)
	}
	if r = do(t, ts, "GET", "/api/schedule/create", "", authed(ts, nil)); r.StatusCode != 405 {
		t.Fatal(r.StatusCode)
	}
}
func TestLiveScheduleAPI(t *testing.T) {
	a, _, _, conv, _, stops := liveGuestWorld(t)
	stops[a]()
	live := NewLive(a)
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	server = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	call := func(method, path string, body any, want int, into any) {
		t.Helper()
		raw := ""
		hdr := authed(ts, nil)
		if body != nil {
			b, e := json.Marshal(body)
			if e != nil {
				t.Fatal(e)
			}
			raw = string(b)
			hdr = post(ts)
		}
		r := do(t, ts, method, path, raw, hdr)
		b, _ := io.ReadAll(r.Body)
		if r.StatusCode != want {
			t.Fatalf("%s %s HTTP%d want%d: %s", method, path, r.StatusCode, want, b)
		}
		if into != nil && json.Unmarshal(b, into) != nil {
			t.Fatalf("invalid JSON %s", b)
		}
	}
	var overview Overview
	call("GET", "/api/overview?topics=1", nil, 200, &overview)
	if !overview.Schedules {
		t.Fatal("missing schedule capability")
	}
	before, _ := a.ViewChanges()
	var audience client.ScheduledAudience
	call("GET", "/api/schedule/audience?conv="+conv, nil, 200, &audience)
	if len(audience.Persons) != 2 {
		t.Fatal(audience)
	}
	var rows []client.ScheduledSend
	call("GET", "/api/schedules", nil, 200, &rows)
	if len(rows) != 0 {
		t.Fatal(rows)
	}
	after, _ := a.ViewChanges()
	if after != before {
		t.Fatal("read-only schedule API changed content")
	}
	stage := func(content string) (string, string) {
		t.Helper()
		id, e := live.StageFile("private.bin", strings.NewReader(content))
		if e != nil {
			t.Fatal(e)
		}
		path := live.staged.files[id].path
		data, e := secfile.Read(path)
		if e != nil || !bytes.Equal(data, []byte(content)) {
			t.Fatal("staging permissions or bytes", len(data), e)
		}
		return id, path
	}
	gone := func(id, path string) {
		t.Helper()
		if _, ok := live.staged.files[id]; ok {
			t.Fatal("stage retained", id)
		}
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Fatal("plaintext staging survived", path, e)
		}
	}
	id := protocol.NewID()
	file, path := stage("private content")
	d := ScheduleDraft{ID: id, Conv: conv, Body: "scheduled API ordinary message", Due: time.Now().Add(time.Hour).UTC(), Timezone: "Europe/Kyiv", Files: []string{file}, Audience: &audience}
	var saved client.ScheduledSend
	_, wake := a.ViewChanges()
	call("POST", "/api/schedule/create", d, 200, &saved)
	gone(file, path)
	select {
	case <-wake:
	default:
		t.Fatal("no content push on durable save")
	}
	encoded, _ := json.Marshal(saved)
	if saved.ID != id || saved.Rev != 1 || saved.State != "pending" || len(saved.Files) != 1 || strings.Contains(string(encoded), path) || strings.Contains(string(encoded), "private content") {
		t.Fatal(saved)
	}
	call("GET", "/api/schedules?id="+id, nil, 200, &saved)
	call("GET", "/api/schedules?conv="+strings.Repeat("e", 64), nil, 200, &rows)
	if len(rows) != 0 {
		t.Fatal("conv filter leaked schedule")
	}
	// A lost response retries the same immutable bytes with a new stage handle.
	file, path = stage("private content")
	d.Files = []string{file}
	before, _ = a.ViewChanges()
	var retried client.ScheduledSend
	call("POST", "/api/schedule/create", d, 200, &retried)
	gone(file, path)
	after, _ = a.ViewChanges()
	if before != after || retried.Rev != saved.Rev || retried.ID != saved.ID {
		t.Fatal("retry wrote or replaced row")
	}
	file, path = stage("different bytes")
	d.Files = []string{file}
	call("POST", "/api/schedule/create", d, 409, nil)
	gone(file, path)
	file, path = stage("refused input")
	d.ID = "bad"
	d.Files = []string{file}
	call("POST", "/api/schedule/create", d, 409, nil)
	gone(file, path)
	file, path = stage("bad due")
	d.ID = protocol.NewID()
	d.Due = time.Now().Add(-time.Hour)
	d.Files = []string{file}
	call("POST", "/api/schedule/create", d, 409, nil)
	gone(file, path)
	for _, p := range []string{"/api/schedules?id=bad", "/api/schedules?all=true", "/api/schedules?all=1&all=0", "/api/schedules?id=" + id + "&all=1", "/api/schedule/audience?conv=" + conv + "&topic=new", "/api/schedules?other=x"} {
		call("GET", p, nil, 409, nil)
	}
	call("GET", "/api/schedules?id="+protocol.NewID(), nil, 404, nil)
	call("POST", "/api/schedule/unknown", map[string]any{}, 404, nil)
	call("POST", "/api/schedule/cancel", map[string]any{"id": id}, 409, nil)
	call("POST", "/api/schedule/edit", map[string]any{"id": id, "rev": 1, "extra": true}, 400, nil)
	call("POST", "/api/schedule/cancel", map[string]any{"id": id, "rev": 1, "conv": conv}, 400, nil)
	call("POST", "/api/schedule/create", map[string]any{"conv": conv, "audience": map[string]any{"persons": []string{}, "pids": []string{}, "authority": true}}, 400, nil)
	edit := map[string]any{"id": id, "rev": saved.Rev, "conv": conv, "body": "edited", "due": time.Now().Add(2 * time.Hour).UTC(), "timezone": "UTC", "files": []string{}}
	var edited client.ScheduledSend
	call("POST", "/api/schedule/edit", edit, 200, &edited)
	if edited.Rev != 2 || len(edited.Files) != 0 {
		t.Fatal(edited)
	}
	call("POST", "/api/schedule/send_now", ScheduleAction{id, saved.Rev}, 409, nil)
	var sent client.ScheduledSend
	call("POST", "/api/schedule/send_now", ScheduleAction{id, edited.Rev}, 200, &sent)
	if sent.State != "sent" {
		t.Fatal("not queued", sent)
	}
	call("GET", "/api/schedules", nil, 200, &rows)
	if len(rows) != 0 {
		t.Fatal("terminal row in active list")
	}
	call("GET", "/api/schedules?all=1", nil, 200, &rows)
	if len(rows) != 1 || rows[0].State != "sent" {
		t.Fatal(rows)
	}
	// A separate pending record can be cancelled through the public adapter.
	var pending client.ScheduledSend
	call("POST", "/api/schedule/create", ScheduleDraft{ID: protocol.NewID(), Conv: conv, Body: "cancel through API", Due: time.Now().Add(time.Hour), Timezone: "UTC"}, 200, &pending)
	var cancelled client.ScheduledSend
	call("POST", "/api/schedule/cancel", ScheduleAction{pending.ID, pending.Rev}, 200, &cancelled)
	if cancelled.State != "cancelled" {
		t.Fatal(cancelled)
	}
	// Default workspace guards apply to every new route.
	if r := do(t, ts, "GET", "/api/schedules", "", nil); r.StatusCode != 401 {
		t.Fatal("cookie bypass", r.StatusCode)
	}
	if r := do(t, ts, "POST", "/api/schedule/cancel", `{"id":"`+id+`","rev":2}`, authed(ts, map[string]string{"Origin": "https://foreign.example", "Content-Type": "application/json"})); r.StatusCode != 403 {
		t.Fatal("origin bypass", r.StatusCode)
	}
	// The exact same ID cannot be looked up through a different workspace provider.
	set := NewWorkspaceProviders()
	one, e := set.Bind(client.Workspace{ID: "default", Name: "One", State: "enrolled"}, live)
	if e != nil {
		t.Fatal(e)
	}
	two, e := set.Bind(client.Workspace{ID: strings.Repeat("b", 32), Name: "Two", State: "enrolled"}, NewFixture(time.Now))
	if e != nil {
		t.Fatal(e)
	}
	handler := server.WorkspaceHandler(set)
	for _, c := range []struct {
		binding WorkspaceBinding
		want    int
	}{{one, 200}, {two, 404}} {
		req := httptest.NewRequest("GET", ts.URL+"/workspaces/"+c.binding.ID+"/"+c.binding.Handle+"/api/schedules?id="+id, nil)
		req.Header.Set("Cookie", cookieName+"="+testToken)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != c.want {
			t.Fatal("workspace schedule routing", rr.Code, rr.Body.String())
		}
	}
}
