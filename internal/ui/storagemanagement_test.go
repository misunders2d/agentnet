package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"

	"github.com/misunders2d/agentnet/internal/client"
)

func TestStorageManagementBrowserEngine(t *testing.T) {
	runNodeCheck(t, "testdata/storage_management_check.mjs")
}

func TestStorageLocalProducerReasonsHidePaths(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&client.FileSizeError{Message: "PRIVATE_PATH is larger than the limit"}, "This file exceeds this device's file-size limit. Choose a smaller file."},
		{&os.PathError{Op: "write", Path: "PRIVATE_PATH", Err: syscall.ENOSPC}, "This device is out of storage. Free disk space, then try again."},
	} {
		if got := sentence(tc.err); got != tc.want {
			t.Fatal(got)
		}
	}
}

func TestStorageManagementRendered(t *testing.T) {
	t.Log(browserCheck(t, "testdata/storage_management_rendered.cjs"))
}

func TestStorageManagementLiveQuotaGuardAndMemberScope(t *testing.T) {
	alice, _, live, _ := liveWorldHub(t)
	call := func(s *Server, method, path, body, origin string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://"+relayStorageHost+path, strings.NewReader(body))
		r.Header.Set("Cookie", cookieName+"="+testToken)
		r.Header.Set("Origin", origin)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		return w
	}
	admin := New(NewLive(alice), relayStorageHost, testToken)
	member := New(live, relayStorageHost, testToken)
	for _, path := range []string{"/api/storage/quota", "/api/storage/cache", "/api/storage/retry_send"} {
		if w := call(admin, "POST", path, `{}`, "https://foreign.example"); w.Code != http.StatusForbidden {
			t.Fatal("cross-origin storage mutation", path, w.Code)
		}
	}
	if w := call(member, "POST", "/api/storage/quota", `{"quota_bytes":2048,"confirm":true}`, "http://"+relayStorageHost); w.Code != 409 || !strings.Contains(w.Body.String(), "Only an admin") {
		t.Fatal("member quota change", w.Code, w.Body.String())
	}
	if w := call(admin, "POST", "/api/storage/quota", `{"quota_bytes":2048,"confirm":false}`, "http://"+relayStorageHost); w.Code != 409 {
		t.Fatal("unconfirmed quota", w.Code, w.Body.String())
	}
	if w := call(admin, "POST", "/api/storage/quota", `{"quota_bytes":2048,"confirm":true}`, "http://"+relayStorageHost); w.Code != 200 || !strings.Contains(w.Body.String(), `"quota_bytes":2048`) {
		t.Fatal("confirmed admin quota", w.Code, w.Body.String())
	}
	if w := call(member, "GET", "/api/storage/cache", ``, "http://"+relayStorageHost); w.Code != 200 {
		t.Fatal("member own-cache preview", w.Code, w.Body.String())
	}
}
