package ui

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestWorkspaceRenameKeepsMountedHandleAndChecksAuthority(t *testing.T) {
	set := NewWorkspaceProviders()
	base := client.Workspace{ID: "default", Name: "Old", Endpoint: "https://fixture.example", Address: "fixture/laptop", Realm: strings.Repeat("b", 32), State: "enrolled"}
	binding, err := set.Bind(base, NewFixture(time.Now))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	set.Rename = func(id, name string) (client.Workspace, error) {
		count++
		renamed := base
		renamed.Name = name
		return renamed, nil
	}
	s := New(NewFixture(time.Now), "127.0.0.1:12345", testToken)
	call := func(handle, origin string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"id": "default", "handle": handle, "name": "New name"})
		r := httptest.NewRequest("POST", "http://127.0.0.1:12345/api/workspaces/rename", strings.NewReader(string(body)))
		r.Header.Set("Cookie", cookieName+"="+testToken)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.WorkspaceHandler(set).ServeHTTP(w, r)
		return w
	}
	if got := call(binding.Handle, "https://foreign.example"); got.Code != 403 {
		t.Fatal("cross-origin rename")
	}
	if got := call(strings.Repeat("c", 32), "http://127.0.0.1:12345"); got.Code != 409 {
		t.Fatal("stale rename")
	}
	if count != 0 {
		t.Fatal("unsafe rename reached registry")
	}
	if got := call(binding.Handle, "http://127.0.0.1:12345"); got.Code != 200 {
		t.Fatal(got.Code, got.Body.String())
	}
	after := set.List()[0]
	if after.Handle != binding.Handle || after.Name != "New name" || after.Endpoint != binding.Endpoint {
		t.Fatal("rename replaced binding")
	}
}
