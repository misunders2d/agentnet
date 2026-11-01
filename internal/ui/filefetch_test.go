package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

type fileFetchFixture struct {
	*Fixture
	calls []string
}

func (f *fileFetchFixture) FetchFile(dir, id string, index int) (client.FileProgress, error) {
	f.calls = append(f.calls, fmt.Sprint("fetch ", dir, " ", id, " ", index))
	return client.FileProgress{Total: 10, Fetching: true}, nil
}

func (f *fileFetchFixture) FileProgress(_ context.Context, dir, id string, index int, after int64) (client.FileProgress, error) {
	f.calls = append(f.calls, fmt.Sprint("progress ", dir, " ", id, " ", index, " after ", after))
	return client.FileProgress{Have: 10, Total: 10, Done: true}, nil
}

// A big received file downloads on a tap (client filewait.go): the page names
// the exact file with its direction, and progress names the bytes it showed.
func TestFileFetchRoutes(t *testing.T) {
	f := &fileFetchFixture{Fixture: NewFixture(time.Now)}
	const host = "127.0.0.1:8123"
	h := New(f, host, testToken).Handler()
	call := func(method, path string) (int, client.FileProgress) {
		var body *strings.Reader
		if method == "POST" {
			body = strings.NewReader(`{}`)
		} else {
			body = strings.NewReader("")
		}
		r := httptest.NewRequest(method, "http://"+host+path, body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", "http://"+host)
		r.Header.Set("Cookie", cookieName+"="+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		var p client.FileProgress
		json.Unmarshal(w.Body.Bytes(), &p)
		return w.Code, p
	}
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", "/api/files/m/2/fetch", 404},          // no direction: never guessed
		{"POST", "/api/files/m/2/fetch?dir=both", 404}, // not a direction
		{"POST", "/api/files/m/-1/fetch?dir=in", 404},
		{"GET", "/api/files/m/2/progress?dir=in&after=x", 404},
		{"POST", "/api/files/m/2/fetch?dir=in", 200},
		{"GET", "/api/files/m/2/progress?dir=out&after=3", 200},
		{"GET", "/api/files/m/1/progress?dir=in", 200}, // after defaults to -1: any state
	} {
		if code, _ := call(tc.method, tc.path); code != tc.status {
			t.Errorf("%s %s: %d, want %d", tc.method, tc.path, code, tc.status)
		}
	}
	want := []string{"fetch in m 2", "progress out m 2 after 3", "progress in m 1 after -1"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatal(f.calls, "want", want)
	}
	if _, p := call("POST", "/api/files/m/0/fetch?dir=in"); p.Total != 10 || !p.Fetching {
		t.Fatal("fetch answer", p)
	}
}
