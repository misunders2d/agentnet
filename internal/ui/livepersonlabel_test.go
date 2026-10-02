package ui

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/testhub"
)

func TestPersonLabelThroughGuardedHandler(t *testing.T) {
	t.Setenv("AGENTNET_NOTIFY", "off")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := filepath.Join(t.TempDir(), "hub")
	testhub.Start(t, dir, "127.0.0.1:0", "")
	a, err := client.Join(ctx, t.TempDir(), testhub.BootstrapCode(t, dir), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	before, err := a.CreatePerson(ctx, "Alice")
	if err != nil {
		t.Fatal(err)
	}
	h := New(NewLive(a), "127.0.0.1:8123", testToken).Handler()
	post := func(body, origin string, authenticated bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "http://127.0.0.1:8123/api/person/label", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", origin)
		if authenticated {
			r.Header.Set("Cookie", cookieName+"="+testToken)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		body, origin string
		auth         bool
		status       int
	}{
		{`{"label":"Wrong"}`, "http://127.0.0.1:8123", false, 401},
		{`{"label":"Wrong"}`, "https://foreign.test", true, 403},
		{`{"label":"Wrong","person":"someone else"}`, "http://127.0.0.1:8123", true, 400},
	} {
		if w := post(tc.body, tc.origin, tc.auth); w.Code != tc.status {
			t.Fatalf("guard: %d %s", w.Code, w.Body)
		}
	}
	unchanged, _, _ := a.Person()
	if unchanged.Roster != before.Roster {
		t.Fatal("refused request changed roster")
	}
	w := post(`{"label":"New display name"}`, "http://127.0.0.1:8123", true)
	var after client.PersonInfo
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &after) != nil {
		t.Fatalf("rename: %d %s", w.Code, w.Body)
	}
	if after.Person != before.Person || after.Fingerprint != before.Fingerprint || after.Address != before.Address || after.Label != "New display name" || after.Seq != before.Seq+1 {
		t.Fatalf("identity/label: %+v", after)
	}
}
