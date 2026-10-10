package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMobilePageKeepsAuthenticatedComicHost(t *testing.T) {
	const host = "127.0.0.1:43871"
	for _, mobile := range []bool{false, true} {
		s := New(NewFixture(time.Now), host, testToken)
		if mobile {
			s.SetMobile()
		}
		for _, path := range []string{"/", "/assets/android.js", "/assets/skins/comic/skin.json"} {
			request := httptest.NewRequest("GET", "http://"+host+path, nil)
			unauthorized := httptest.NewRecorder()
			s.Handler().ServeHTTP(unauthorized, request)
			if unauthorized.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated %s: %d", path, unauthorized.Code)
			}
			request.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
			response := httptest.NewRecorder()
			s.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("authenticated %s: %d", path, response.Code)
			}
			if path == "/" {
				body := response.Body.String()
				if strings.Contains(body, "/assets/android.js") != mobile || !strings.Contains(body, "/assets/loader.js") {
					t.Fatalf("incorrect mobile bootstrap (mobile=%v)", mobile)
				}
				if strings.Contains(body, "device.mjs") || strings.Contains(body, "engine.mjs") {
					t.Fatal("native phone must not create a second browser identity/engine")
				}
				if !strings.Contains(response.Header().Get("Content-Security-Policy"), "script-src 'self'") {
					t.Fatal("native host lost script policy")
				}
			}
		}
	}
}

func TestMobileBlobFetchPolicyLeavesDesktopStrict(t *testing.T) {
	const host = "127.0.0.1:43871"
	desktop := New(NewFixture(time.Now), host, testToken)
	mobile := New(NewFixture(time.Now), host, testToken)
	mobile.SetMobile()
	cases := []struct {
		name    string
		handler http.Handler
		blob    bool
	}{
		{"desktop", desktop.Handler(), false}, {"mobile", mobile.Handler(), true},
		{"desktop setup", NewSetup(nil, host, testToken), false}, {"mobile setup", NewMobileSetup(nil, host, testToken), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://"+host+"/", nil)
			unauthorized := httptest.NewRecorder()
			tc.handler.ServeHTTP(unauthorized, req)
			if unauthorized.Code != http.StatusUnauthorized {
				t.Fatalf("auth bypass: %d", unauthorized.Code)
			}
			req.AddCookie(&http.Cookie{Name: cookieName, Value: testToken})
			response := httptest.NewRecorder()
			tc.handler.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatal(response.Code)
			}
			policy := response.Header().Get("Content-Security-Policy")
			want := "connect-src 'self'"
			if tc.blob {
				want += " blob:"
			}
			var connect string
			for _, directive := range strings.Split(policy, ";") {
				if strings.HasPrefix(strings.TrimSpace(directive), "connect-src ") {
					connect = strings.TrimSpace(directive)
				}
			}
			if connect != want {
				t.Fatalf("connect policy %q want %q", connect, want)
			}
			if !strings.Contains(policy, "script-src 'self';") || !strings.Contains(policy, "frame-ancestors 'none'") {
				t.Fatalf("weakened page policy: %s", policy)
			}
			req.Host = "attacker.example"
			wrongHost := httptest.NewRecorder()
			tc.handler.ServeHTTP(wrongHost, req)
			if wrongHost.Code != http.StatusMisdirectedRequest {
				t.Fatalf("wrong host accepted: %d", wrongHost.Code)
			}
		})
	}
}
