package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
)

type storageFixture struct {
	*Fixture
	view  client.StorageSummary
	err   error
	calls int
}

func (f *storageFixture) Storage(context.Context) (client.StorageSummary, error) {
	f.calls++
	return f.view, f.err
}

func TestStoragePageHandlerUsesExistingGuardAndOptionalProvider(t *testing.T) {
	f := &storageFixture{Fixture: NewFixture(time.Now), view: client.StorageSummary{
		Local:  client.LocalStorage{Complete: true, Known: client.StorageAmount{Files: 2, Bytes: 17}},
		Remote: client.RemoteStorage{Status: "unsupported", Reason: "older Hub"},
	}}
	s := New(f, "127.0.0.1:8123", "synthetic-storage-session")
	handler := s.guard(http.HandlerFunc(s.storage))
	for _, tc := range []struct {
		method, host string
		auth         bool
		want         int
	}{
		{http.MethodGet, "127.0.0.1:8123", false, http.StatusUnauthorized},
		{http.MethodGet, "untrusted.invalid", true, http.StatusMisdirectedRequest},
		{http.MethodGet, "127.0.0.1:8123", true, http.StatusOK},
		{http.MethodPost, "127.0.0.1:8123", true, http.StatusMethodNotAllowed},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/storage", nil)
		if tc.auth {
			r.AddCookie(&http.Cookie{Name: cookieName, Value: "synthetic-storage-session"})
		}
		if tc.method == http.MethodPost {
			r.Header.Set("Origin", "http://127.0.0.1:8123")
			r.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		before := f.calls
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("storage guard: %d %s", w.Code, w.Body)
		}
		if tc.want != http.StatusOK && f.calls != before {
			t.Fatal("denied/mutating request inspected storage")
		}
		if tc.want == http.StatusOK {
			var got client.StorageSummary
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Local.Known.Bytes != 17 || got.Remote.Status != "unsupported" {
				t.Fatalf("provider summary hidden: %+v %v", got, err)
			}
		}
	}
	f.err = errors.New("/private/secret token=synthetic-private-value")
	w := httptest.NewRecorder()
	s.storage(w, httptest.NewRequest(http.MethodGet, "/api/storage", nil))
	if w.Code != http.StatusInternalServerError || strings.Contains(w.Body.String(), "synthetic-private-value") || strings.Contains(w.Body.String(), "/private") {
		t.Fatalf("storage provider error disclosed: %d %s", w.Code, w.Body)
	}
	demo := New(NewFixture(time.Now), "127.0.0.1:8123", "synthetic")
	w = httptest.NewRecorder()
	demo.storage(w, httptest.NewRequest(http.MethodGet, "/api/storage", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unsupported provider fabricated a view: %d %s", w.Code, w.Body)
	}
}

func TestLiveStorageKeepsStagingAndOfflineLocalUsage(t *testing.T) {
	_, _, live, hub := liveWorldHub(t)
	file, err := live.StageFile("synthetic.txt", strings.NewReader("three"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.DiscardFiles([]string{file})
	hub.Stop()
	view, err := live.Storage(context.Background())
	if err != nil || view.Remote.Status != "unavailable" || !view.Local.Complete || view.Local.Known.Bytes != 5 || view.Local.Known.Files != 1 {
		t.Fatalf("offline Hub hid staged bytes: %+v %v", view, err)
	}
	live.staged.mu.Lock()
	_, remains := live.staged.files[file]
	live.staged.mu.Unlock()
	if !remains {
		t.Fatal("storage inspection discarded staging")
	}
}
