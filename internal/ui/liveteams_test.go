package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

type teamFixture struct {
	*Fixture
	view     client.TeamsView
	state    protocol.TeamState
	snapshot protocol.TeamSnapshot
	err      error
	calls    int
	change   client.TeamChange
	selected []string
}

func (f *teamFixture) Teams(context.Context) (client.TeamsView, error) {
	f.calls++
	return f.view, f.err
}
func (f *teamFixture) ChangeTeam(_ context.Context, c client.TeamChange) (protocol.TeamState, error) {
	f.calls++
	f.change = c
	return f.state, f.err
}
func (f *teamFixture) TeamSnapshot(_ context.Context, ids []string) (protocol.TeamSnapshot, error) {
	f.calls++
	f.selected = ids
	return f.snapshot, f.err
}

func TestTeamsProviderGuardSelectionAndTruth(t *testing.T) {
	id, person, realm := protocol.NewID(), protocol.NewID(), protocol.NewID()
	f := &teamFixture{Fixture: NewFixture(time.Now), state: protocol.TeamState{RealmID: realm, ID: id, Name: "Ops", Members: []string{person}, Managers: []string{person}}}
	f.view = client.TeamsView{RealmID: realm, Status: "available", Current: true, Teams: []client.TeamView{{TeamState: f.state, Member: true, Manager: true, Listed: true}}}
	f.snapshot = protocol.TeamSnapshot{RealmID: realm, Sources: []protocol.TeamRef{{ID: id, Seq: 2, Hash: strings.Repeat("a", 64)}}, Persons: []protocol.PersonRef{{ID: person, Seq: 0, Hash: strings.Repeat("b", 64)}}}
	s := New(f, "127.0.0.1:8123", testToken)
	handler := s.Handler()
	request := func(method, path, body, origin string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1:8123"+path, strings.NewReader(body))
		if auth {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
		}
		if method == http.MethodPost {
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		method, path, body, origin string
		auth                       bool
		want                       int
	}{
		{"GET", "/api/teams", "", "", false, 401},
		{"POST", "/api/team", `{"op":"join","team":"` + id + `"}`, "http://untrusted.invalid", true, 403},
		{"POST", "/api/teams/snapshot", `{"teams":[],"invite":true}`, "http://127.0.0.1:8123", true, 400},
		{"POST", "/api/teams", "{}", "http://127.0.0.1:8123", true, 405},
	} {
		before := f.calls
		w := request(tc.method, tc.path, tc.body, tc.origin, tc.auth)
		if w.Code != tc.want || before != f.calls {
			t.Fatalf("guard reached provider: %d %s calls=%d", w.Code, w.Body, f.calls)
		}
	}
	w := request("GET", "/api/teams", "", "", true)
	var view client.TeamsView
	if err := json.Unmarshal(w.Body.Bytes(), &view); w.Code != 200 || err != nil || !view.Current || !view.Teams[0].Member {
		t.Fatalf("directory: %d %s %v", w.Code, w.Body, err)
	}
	w = request("POST", "/api/team", `{"op":"join","team":"`+id+`"}`, "http://127.0.0.1:8123", true)
	if w.Code != 200 || f.change.Op != protocol.TeamJoin || f.change.Team != id {
		t.Fatalf("change: %d %s %+v", w.Code, w.Body, f.change)
	}
	w = request("POST", "/api/teams/snapshot", `{"teams":["`+id+`"]}`, "http://127.0.0.1:8123", true)
	var snapshot protocol.TeamSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &snapshot); w.Code != 200 || err != nil || !reflect.DeepEqual(f.selected, []string{id}) || len(snapshot.Persons) != 1 {
		t.Fatalf("snapshot: %d %s %v", w.Code, w.Body, err)
	}
	// An unavailable provider retains its verified membership and sanitized
	// current-state reason; provider error details never reach the browser.
	f.err = errors.New("token=synthetic-private-value /private/path")
	f.view.Current = false
	f.view.Status = "unavailable"
	f.view.Reason = "offline; retained membership"
	w = request("GET", "/api/teams", "", "", true)
	if err := json.Unmarshal(w.Body.Bytes(), &view); w.Code != 200 || err != nil || view.Current || !view.Teams[0].Member || strings.Contains(w.Body.String(), "synthetic-private-value") {
		t.Fatalf("offline truth: %d %s %v", w.Code, w.Body, err)
	}
	w = request("POST", "/api/team", `{"op":"leave","team":"`+id+`"}`, "http://127.0.0.1:8123", true)
	if w.Code < 400 || strings.Contains(w.Body.String(), "synthetic-private-value") || !strings.Contains(w.Body.String(), "not confirmed") {
		t.Fatalf("unsafe mutation result: %d %s", w.Code, w.Body)
	}
	f.err = protocol.ErrTeamLastManager
	w = request("POST", "/api/team", `{"op":"leave","team":"`+id+`"}`, "http://127.0.0.1:8123", true)
	if w.Code < 400 || !strings.Contains(w.Body.String(), "last manager") {
		t.Fatalf("last manager explanation: %d %s", w.Code, w.Body)
	}
	demo := New(NewFixture(time.Now), "127.0.0.1:8123", testToken)
	w = httptest.NewRecorder()
	demo.teams(w, httptest.NewRequest("GET", "/api/teams", nil))
	if w.Code != 404 {
		t.Fatalf("unsupported provider invented directory: %d %s", w.Code, w.Body)
	}
}

func TestTeamsLiveProviderDirectoryAndSnapshot(t *testing.T) {
	_, _, live, _ := liveWorldHub(t)
	if _, _, err := live.CreatePerson("Alice"); err != nil {
		t.Fatal(err)
	}
	state, err := live.ChangeTeam(context.Background(), client.TeamChange{Op: protocol.TeamCreate, Name: "Ops"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := live.Teams(context.Background())
	for deadline := time.Now().Add(10 * time.Second); err == nil && !view.Current && time.Now().Before(deadline); {
		time.Sleep(25 * time.Millisecond)
		view, err = live.Teams(context.Background())
	}
	if err != nil || !view.Current || len(view.Teams) != 1 || !view.Teams[0].Manager {
		t.Fatalf("live view: %+v %v", view, err)
	}
	snapshot, err := live.TeamSnapshot(context.Background(), []string{state.ID, state.ID})
	for deadline := time.Now().Add(10 * time.Second); errors.Is(err, client.ErrTeamStale) && time.Now().Before(deadline); {
		time.Sleep(25 * time.Millisecond)
		snapshot, err = live.TeamSnapshot(context.Background(), []string{state.ID, state.ID})
	}
	if err != nil || len(snapshot.Sources) != 1 || len(snapshot.Persons) != 1 {
		t.Fatalf("live selection: %+v %v", snapshot, err)
	}
}
