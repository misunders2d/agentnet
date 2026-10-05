package googleauth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Desktop uses the system browser, S256 PKCE and a single-use loopback
// callback. Exchange runs at the relay; an optional Desktop client secret
// stays there. No refresh token is requested; no Google token is kept.
func Desktop(ctx context.Context, clientID, nonce string, exchange func(context.Context, string, string, string) (string, error), complete func(string, error)) (string, func(), error) {
	if clientID == "" || nonce == "" {
		return "", nil, errors.New("workspace admin must configure Google Desktop sign-in")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, errors.New("cannot start Google sign-in callback")
	}
	redirect := "http://" + ln.Addr().String() + "/oauth/google"
	state, verifier := oauth2.GenerateVerifier(), oauth2.GenerateVerifier()
	cfg := oauth2.Config{ClientID: clientID, RedirectURL: redirect, Scopes: []string{"openid", "email", "profile"}, Endpoint: google.Endpoint}
	auth := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("nonce", nonce), oauth2.SetAuthURLParam("prompt", "select_account"))
	flow, cancel := context.WithTimeout(ctx, 5*time.Minute)
	var used sync.Once
	var srv *http.Server
	finish := func(token string, err error) { used.Do(func() { complete(token, err); cancel() }) }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/google", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.Host != ln.Addr().String() || r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid sign-in state", 400)
			return
		}
		if r.URL.Query().Get("error") != "" {
			finish("", ErrCredential)
			http.Error(w, "Sign-in declined. Return to AgentNet.", 403)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" || len(code) > 4096 {
			http.Error(w, "Missing sign-in code", 400)
			return
		}
		// Consume before exchange, including failures. A duplicate callback
		// cannot execute another enrollment or exchange.
		ran := false
		used.Do(func() {
			ran = true
			token, err := exchange(flow, code, verifier, redirect)
			complete(token, err)
			cancel()
		})
		if !ran {
			http.Error(w, "Sign-in already completed", 409)
			return
		}
		io.WriteString(w, "Return to AgentNet. Your sign-in result appears there.")
	})
	srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	go srv.Serve(ln)
	go func() {
		<-flow.Done()
		finish("", ErrCredential)
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		srv.Shutdown(shutdown)
	}()
	return auth, func() { finish("", ErrCredential) }, nil
}
