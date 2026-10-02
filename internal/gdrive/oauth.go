package gdrive

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type OAuthConfig struct {
	ClientID     string       `json:"client_id"`
	ClientSecret string       `json:"-"`
	AuthURL      string       `json:"-"`
	TokenURL     string       `json:"-"`
	RevokeURL    string       `json:"-"`
	HTTP         *http.Client `json:"-"`
}
type Token struct {
	Access  string    `json:"access_token"`
	Refresh string    `json:"refresh_token,omitempty"`
	Scope   string    `json:"scope"`
	Expiry  time.Time `json:"expiry"`
}

func (t Token) Has(scope string) bool {
	for _, s := range strings.Fields(t.Scope) {
		if s == scope {
			return true
		}
	}
	return false
}
func (o OAuthConfig) endpoint(kind string) string {
	switch kind {
	case "auth":
		if o.AuthURL != "" {
			return o.AuthURL
		}
		return "https://accounts.google.com/o/oauth2/v2/auth"
	case "token":
		if o.TokenURL != "" {
			return o.TokenURL
		}
		return "https://oauth2.googleapis.com/token"
	default:
		if o.RevokeURL != "" {
			return o.RevokeURL
		}
		return "https://oauth2.googleapis.com/revoke"
	}
}
func random() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// Consent is single-use Desktop-app PKCE consent on a random loopback port.
// The initiating UI gets only URL; tokens are handed to its local Save callback.
// Caller context must cover the whole consent flow, not just a page request.
func (o OAuthConfig) Consent(ctx context.Context, full bool, save func(Token) error) (string, error) {
	if strings.TrimSpace(o.ClientID) == "" {
		return "", errors.New("configure a Google Desktop OAuth client first")
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", errors.New("cannot start Google loopback consent")
	}
	redirect := "http://" + l.Addr().String() + "/oauth/google"
	state, verifier := random(), oauth2.GenerateVerifier()
	scope := FileScope
	if full {
		scope = FullScope
	}
	cfg := oauth2.Config{ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: redirect, Scopes: []string{scope}, Endpoint: google.Endpoint}
	cfg.Endpoint.AuthURL = o.endpoint("auth")
	cfg.Endpoint.TokenURL = o.endpoint("token")
	authURL := cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent select_account"))
	done := make(chan struct{})
	var used atomic.Bool
	var srv *http.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/google", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.Method != "GET" || r.URL.Query().Get("state") != state {
			http.Error(w, "Invalid consent state", 400)
			return
		}
		if !used.CompareAndSwap(false, true) {
			http.Error(w, "Consent already used", 409)
			return
		}
		defer close(done)

		if r.URL.Query().Get("error") != "" {
			http.Error(w, "Google consent declined. Return to AgentNet.", 403)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" || len(code) > 4096 {
			http.Error(w, "Missing Google consent code", 400)
			return
		}
		t, e := o.exchange(ctx, url.Values{"client_id": {o.ClientID}, "client_secret": {o.ClientSecret}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "grant_type": {"authorization_code"}})
		if e == nil && !t.Has(scope) {
			e = ErrConsent
		}
		if e == nil {
			e = save(t)
		}
		if e != nil {
			http.Error(w, "Google connection failed. Return to AgentNet and reconnect.", 400)
			return
		}
		io.WriteString(w, "Google connected on this device. Return to AgentNet.")
	})
	srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 10 * time.Second}
	go srv.Serve(l)
	go func() {
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Minute):
		case <-done:
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	return authURL, nil
}
func (o OAuthConfig) exchange(ctx context.Context, q url.Values) (Token, error) {
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	safe := *hc
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &safe)
	cfg := oauth2.Config{ClientID: o.ClientID, ClientSecret: o.ClientSecret, RedirectURL: q.Get("redirect_uri"), Endpoint: google.Endpoint}
	cfg.Endpoint.TokenURL = o.endpoint("token")
	var token *oauth2.Token
	var err error
	if q.Get("grant_type") == "refresh_token" {
		token, err = cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: q.Get("refresh_token"), Expiry: time.Now().Add(-time.Hour)}).Token()
	} else {
		token, err = cfg.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(q.Get("code_verifier")))
	}
	if err != nil {
		var provider *oauth2.RetrieveError
		if errors.As(err, &provider) {
			return Token{}, ErrConsent
		}
		var network net.Error
		if errors.As(err, &network) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Token{}, ErrOffline
		}
		return Token{}, ErrConsent
	}
	if token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.Expiry.IsZero() || !token.Expiry.After(time.Now()) {
		return Token{}, ErrConsent
	}
	scope, _ := token.Extra("scope").(string)
	return Token{Access: token.AccessToken, Refresh: token.RefreshToken, Scope: scope, Expiry: token.Expiry}, nil
}
func (o OAuthConfig) Refresh(ctx context.Context, t Token) (Token, error) {
	if t.Refresh == "" {
		return Token{}, ErrConsent
	}
	n, e := o.exchange(ctx, url.Values{"client_id": {o.ClientID}, "client_secret": {o.ClientSecret}, "refresh_token": {t.Refresh}, "grant_type": {"refresh_token"}})
	if e == nil {
		if n.Refresh == "" {
			n.Refresh = t.Refresh
		}
		if n.Scope == "" {
			n.Scope = t.Scope
		}
	}
	return n, e
}
func (o OAuthConfig) Revoke(ctx context.Context, t Token) error {
	token := t.Refresh
	if token == "" {
		token = t.Access
	}
	if token == "" {
		return nil
	}
	req, e := http.NewRequestWithContext(ctx, "POST", o.endpoint("revoke"), strings.NewReader(url.Values{"token": {token}}.Encode()))
	if e != nil {
		return ErrConsent
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	hc := o.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	safe := *hc
	safe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, e := safe.Do(req)
	if e != nil {
		return ErrOffline
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return ErrConsent
	}
	return nil
}
