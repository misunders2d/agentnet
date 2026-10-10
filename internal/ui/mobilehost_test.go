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
