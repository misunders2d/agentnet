package googleauth

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestGoogleDesktopPKCEAndState(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan error, 1)
	auth, stop, err := Desktop(ctx, "desktop.apps.googleusercontent.com", "device-nonce", func(ctx context.Context, code, verifier, redirect string) (string, error) {
		if code != "fixture-code" || len(verifier) < 43 || !strings.HasPrefix(redirect, "http://127.0.0.1:") {
			t.Error("bad exchange")
		}
		return "fixture-id-token", nil
	}, func(token string, err error) {
		if err == nil && token != "fixture-id-token" {
			t.Error("lost ID token")
		}
		completed <- err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	u, _ := url.Parse(auth)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("nonce") != "device-nonce" || q.Get("scope") != "openid email profile" || q.Get("access_type") == "offline" || q.Get("state") == "" {
		t.Fatal("unsafe authorization parameters")
	}
	callback := q.Get("redirect_uri")
	r, err := http.Get(callback + "?state=wrong&code=fixture-code")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 400 {
		t.Fatal("wrong state accepted")
	}
	select {
	case <-completed:
		t.Fatal("wrong state consumed sign-in")
	default:
	}
	r, err = http.Get(callback + "?state=" + url.QueryEscape(q.Get("state")) + "&code=fixture-code")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}
