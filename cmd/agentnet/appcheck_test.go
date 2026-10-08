package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
)

func appCheckRequest(t *testing.T, r *appRunner, ctx context.Context) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+r.addr+"/api/app/check", nil).WithContext(ctx)
	req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: r.token})
	rec := httptest.NewRecorder()
	if !r.appAPI(rec, req) {
		t.Fatal("app check did not use the app API")
	}
	return rec
}

func TestAppCheckReportsLatest(t *testing.T) {
	previous := protocol.Version
	t.Cleanup(func() { protocol.Version = previous })
	r := &appRunner{home: t.TempDir(), addr: "127.0.0.1:17443", token: "private"}
	for _, tc := range []struct{ current, latest, state string }{
		{"v0.8.9", "v0.8.10", "available"},
		{"v0.8.10", "v0.8.10", "current"},
		{"v0.8.11", "v0.8.10", "ahead"},
		{"v0.8.10+abc123", "v0.8.10", "ahead"},
		{"v0.8.11-2-gabcdef-dirty", "v0.8.10", "ahead"},
		{"v0.8.9-dirty", "v0.8.10", "available"},
		{"v0.9.99", "v0.10.0", "available"},
	} {
		t.Run(tc.current, func(t *testing.T) {
			fakeReleaseServer(t, &releaseStub{latest: tc.latest})
			protocol.Version = tc.current
			rec := appCheckRequest(t, r, t.Context())
			var got map[string]string
			want := map[string]string{"version": tc.current, "latest": tc.latest, "state": tc.state}
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("check: %d %s, want %+v", rec.Code, rec.Body, want)
			}
			if rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("availability response may be cached")
			}
		})
	}
}

type appCheckTransport func(*http.Request) (*http.Response, error)

func (f appCheckTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAppCheckRequiresExactHostCookieAndMethod(t *testing.T) {
	previous := updateClient
	t.Cleanup(func() { updateClient = previous })
	updateClient = &http.Client{Transport: appCheckTransport(func(*http.Request) (*http.Response, error) {
		t.Error("refused check reached the release origin")
		return nil, errors.New("unexpected lookup")
	})}
	r := &appRunner{addr: "127.0.0.1:17443", token: "private"}
	for _, tc := range []struct {
		name, host, cookie, method string
		code                       int
	}{
		{"no cookie", r.addr, "", "GET", 401},
		{"wrong cookie", r.addr, "wrong", "GET", 401},
		{"rebound host", "evil.test", r.token, "GET", 401},
		{"post", r.addr, r.token, "POST", 405},
		{"head", r.addr, r.token, "HEAD", 405},
		{"delete", r.addr, r.token, "DELETE", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+"/api/app/check", strings.NewReader(`{}`))
			req.Header.Set("Origin", "http://"+r.addr)
			req.Header.Set("Content-Type", "application/json")
			if tc.cookie != "" {
				req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: tc.cookie})
			}
			rec := httptest.NewRecorder()
			r.appAPI(rec, req)
			if rec.Code != tc.code || tc.code == 405 && rec.Header().Get("Allow") != "GET" {
				t.Fatalf("check: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestAppCheckFailureIsNotCurrent(t *testing.T) {
	previous := protocol.Version
	t.Cleanup(func() { protocol.Version = previous })
	r := &appRunner{addr: "127.0.0.1:17443", token: "private"}
	for _, tc := range []struct {
		name, version, tag, problem string
		code                        int
	}{
		{"unknown build", "dev", "v0.8.10", "Cannot compare", 409},
		{"unknown prerelease", "v0.8.10-rc.1", "v0.8.10", "Cannot compare", 409},
		{"invalid tag", "v0.8.9", "v0.8.10-rc.1", "not vX.Y.Z", 502},
		{"offline", "v0.8.9", "v0.8.10", "offline fixture", 502},
		{"timeout", "v0.8.9", "v0.8.10", "deadline exceeded", 502},
		{"not redirect", "v0.8.9", "v0.8.10", "unexpected answer", 502},
		{"cancelled caller", "v0.8.9", "v0.8.10", "context canceled", 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeReleaseServer(t, &releaseStub{latest: tc.tag})
			protocol.Version = tc.version
			transport := updateClient.Transport
			updateClient.Transport = appCheckTransport(func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				if !ok || time.Until(deadline) > 15*time.Second {
					t.Error("lookup lacks its short deadline")
				}
				if req.Method != "GET" || req.URL.String() != releaseBase+"/latest" {
					t.Errorf("unexpected request %s %s", req.Method, req.URL)
				}
				switch tc.name {
				case "offline":
					return nil, errors.New("offline fixture")
				case "timeout":
					return nil, context.DeadlineExceeded
				case "not redirect":
					return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
				}
				return transport.RoundTrip(req)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.name == "cancelled caller" {
				cancel()
			}
			rec := appCheckRequest(t, r, ctx)
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.problem) || strings.Contains(rec.Body.String(), `"state":"current"`) {
				t.Fatalf("failure: %d %s", rec.Code, rec.Body)
			}
		})
	}
}

func TestAppCheckLeavesBusyUnsupportedAppUntouched(t *testing.T) {
	f := newGlobalUpdateFixture(t)
	fakeReleaseServer(t, &releaseStub{latest: "v1.2.4"})
	transport := updateClient.Transport
	lookups := 0
	updateClient.Transport = appCheckTransport(func(req *http.Request) (*http.Response, error) {
		lookups++
		if req.Method != "GET" || req.URL.String() != releaseBase+"/latest" {
			t.Errorf("check downloaded instead of looking up: %s %s", req.Method, req.URL)
		}
		return transport.RoundTrip(req)
	})
	// Occupy the same fence that active jobs hold against app updates.
	// Any attempt to pause must fail, and the check must not release it.
	a, _ := diagnosticAgent(t)
	resume, err := a.PauseForAppUpdate()
	if err != nil {
		t.Fatal(err)
	}
	defer resume()
	f.runner.activeAgent.Store(a)
	f.runner.exe = filepath.Join(t.TempDir(), "absent-unpackaged-app")
	independent := &appIndependentDaemon{Exe: "unchanged"}
	f.runner.independent = independent
	if err := writeAppUpdateResult(f.home, protocol.Version, "pending", "Unchanged result"); err != nil {
		t.Fatal(err)
	}
	before := f.snapshot(t)
	for _, updating := range []bool{false, true} {
		f.runner.updating.Store(updating)
		rec := appCheckRequest(t, f.runner, t.Context())
		if rec.Code != 200 || f.runner.updating.Load() != updating || f.runner.independent != independent {
			t.Fatalf("check changed update state: %d %s", rec.Code, rec.Body)
		}
	}
	if release, err := a.PauseForAppUpdate(); err == nil {
		release()
		t.Fatal("check released the busy execution fence")
	}
	if !reflect.DeepEqual(before, f.snapshot(t)) || lookups != 2 {
		t.Fatalf("check changed fixture files or repeated/downloaded: lookups=%d", lookups)
	}
}
