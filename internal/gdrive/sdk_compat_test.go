package gdrive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filippo.io/age"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSDKRefreshSchemaScopesAndSecret(t *testing.T) {
	for _, secret := range []string{"", "local-only-secret"} {
		t.Run(fmt.Sprintf("secret-length-%d", len(secret)), func(t *testing.T) {
			calls := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				r.ParseForm()
				if r.Form.Get("client_secret") != secret {
					t.Error("wrong local secret")
				}
				if secret == "" {
					if _, present := r.Form["client_secret"]; present {
						t.Error("empty secret field sent")
					}
				}
				if r.Form.Get("refresh_token") != "old-refresh" || r.Form.Get("grant_type") != "refresh_token" {
					t.Error("wrong grant")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"access_token":"next-access","expires_in":3600,"token_type":"Bearer"}`)
			}))
			defer provider.Close()
			old := Token{Access: "old-access", Refresh: "old-refresh", Scope: FileScope, Expiry: time.Now().Add(-time.Hour)}
			next, err := (OAuthConfig{ClientID: "fixture", ClientSecret: secret, TokenURL: provider.URL}).Refresh(context.Background(), old)
			if err != nil || next.Refresh != old.Refresh || next.Scope != old.Scope || next.Access != "next-access" || calls != 1 {
				t.Fatalf("refresh compatibility %+v %v calls%d", next, err, calls)
			}
			raw, _ := json.Marshal(next)
			var stored map[string]any
			json.Unmarshal(raw, &stored)
			if len(stored) != 4 || stored["access_token"] != next.Access || stored["refresh_token"] != next.Refresh || stored["scope"] != next.Scope || stored["expiry"] == nil {
				t.Fatal("durable token schema changed")
			}
			var reloaded Token
			if err := json.Unmarshal(raw, &reloaded); err != nil || !reloaded.Expiry.Equal(next.Expiry) || reloaded.Scope != old.Scope {
				t.Fatal("restart schema mismatch")
			}
		})
	}
}

func TestSDKRefreshRotationAndProviderRedaction(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"next","refresh_token":"rotated","scope":"https://www.googleapis.com/auth/drive","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer provider.Close()
	next, e := (OAuthConfig{ClientID: "fixture", TokenURL: provider.URL}).Refresh(context.Background(), Token{Refresh: "old", Scope: FileScope})
	if e != nil || next.Refresh != "rotated" || !next.Has(FullScope) {
		t.Fatalf("rotation %+v %v", next, e)
	}
	for _, status := range []int{400, 401, 500} {
		p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			io.WriteString(w, `{"error":"invalid_grant","error_description":"SECRET fixture raw"}`)
		}))
		_, e := (OAuthConfig{ClientID: "fixture", TokenURL: p.URL}).Refresh(context.Background(), Token{Refresh: "old"})
		p.Close()
		if !errors.Is(e, ErrConsent) || strings.Contains(e.Error(), "SECRET") {
			t.Fatalf("provider leak %v", e)
		}
	}
}

func TestSDKMutationsSingleAttemptAndNoDebugBodies(t *testing.T) {
	var logs strings.Builder
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prior)
	for _, status := range []int{401, 403, 404, 429, 500, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			posts := 0
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer LOCAL-token" {
					t.Error("explicit token transport missing")
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == "GET" {
					io.WriteString(w, `{"id":"folder","mimeType":"application/vnd.google-apps.folder","capabilities":{"canAddChildren":true,"canShare":true}}`)
					return
				}
				posts++
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"SECRET body","code":500}}`)
			}))
			defer provider.Close()
			c := Client{API: provider.URL, UploadAPI: provider.URL, Token: func(context.Context) (string, error) { return "LOCAL-token", nil }}
			for _, action := range []func() error{
				func() error { _, e := c.CreateFolder(context.Background(), "PRIVATE-folder"); return e },
				func() error {
					_, e := c.Upload(context.Background(), "folder", "PRIVATE-file", "", strings.NewReader("PRIVATE-content"), 15)
					return e
				},
				func() error {
					_, e := c.Share(context.Background(), "folder", "PRIVATE@example.test", "writer")
					return e
				},
			} {
				before := posts
				e := action()
				if e == nil || posts != before+1 || strings.Contains(e.Error(), "SECRET") {
					t.Fatalf("mutation repeated/leaked posts%d before%d error%v", posts, before, e)
				}
			}
		})
	}
	for _, secret := range []string{"LOCAL-token", "PRIVATE-", "SECRET"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("SDK debug leaked private fields")
		}
	}
}

func TestSDKTokenRedirectNeverFollowed(t *testing.T) {
	reached := false
	sink := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer sink.Close()
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, sink.URL, 307) }))
	defer p.Close()
	_, e := (OAuthConfig{ClientID: "fixture", TokenURL: p.URL}).Refresh(context.Background(), Token{Refresh: "secret"})
	if e == nil || reached {
		t.Fatal("token followed redirect")
	}

}

type acceptedLostTransport struct{ base http.RoundTripper }

func (t acceptedLostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	res, e := t.base.RoundTrip(r)
	if e == nil && r.Method == "POST" {
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return nil, errors.New("synthetic accepted response lost")
	}
	return res, e
}
func TestSDKAcceptedResponseLostNeverRepeats(t *testing.T) {
	posts := 0
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"id":"folder","mimeType":"application/vnd.google-apps.folder","capabilities":{"canAddChildren":true,"canShare":true}}`)
			return
		}
		posts++
		io.WriteString(w, `{"id":"accepted"}`)
	}))
	defer p.Close()
	c := Client{API: p.URL, UploadAPI: p.URL, HTTP: &http.Client{Transport: acceptedLostTransport{http.DefaultTransport}}, Token: func(context.Context) (string, error) { return "fixture", nil }}
	_, e := c.CreateFolder(context.Background(), "Created")
	if !errors.Is(e, ErrOffline) || posts != 1 {
		t.Fatalf("ambiguous create retried %d %v", posts, e)
	}
	_, e = c.Upload(context.Background(), "folder", "file", "", strings.NewReader("x"), 1)
	if !errors.Is(e, ErrOffline) || posts != 2 {
		t.Fatalf("ambiguous upload retried %d %v", posts, e)
	}
	_, e = c.Share(context.Background(), "folder", "person@example.test", "reader")
	if !errors.Is(e, ErrOffline) || posts != 3 {
		t.Fatalf("ambiguous permission retried %d %v", posts, e)
	}
}
func TestSDKMalformedTokenStillConsentError(t *testing.T) {
	p := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "invalid SECRET payload")
	}))
	defer p.Close()
	_, e := (OAuthConfig{ClientID: "fixture", TokenURL: p.URL}).Refresh(context.Background(), Token{Refresh: "old"})
	if !errors.Is(e, ErrConsent) || strings.Contains(e.Error(), "SECRET") {
		t.Fatalf("malformed token semantics %v", e)
	}
}

func TestSDKEncryptedTokenSchemaRoundtrip(t *testing.T) {
	id, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	old := Token{Access: "fixture-only-access", Refresh: "fixture-only-refresh", Scope: FileScope, Expiry: time.Now().Add(time.Hour).UTC()}
	raw, e := json.Marshal(old)
	if e != nil {
		t.Fatal(e)
	}
	var ct bytes.Buffer
	w, e := age.Encrypt(&ct, id.Recipient())
	if e != nil {
		t.Fatal(e)
	}
	w.Write(raw)
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	r, e := age.Decrypt(bytes.NewReader(ct.Bytes()), id)
	if e != nil {
		t.Fatal(e)
	}
	clear, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	var recovered Token
	if e = json.Unmarshal(clear, &recovered); e != nil || recovered.Access != old.Access || recovered.Refresh != old.Refresh || recovered.Scope != old.Scope || !recovered.Expiry.Equal(old.Expiry) {
		t.Fatal("existing age-encrypted JSON schema incompatible", e)
	}
}

func TestSDKWrappedConsentRedacted(t *testing.T) {
	c := Client{API: "http://127.0.0.1:1", Token: func(context.Context) (string, error) { return "", fmt.Errorf("SECRET caller detail: %w", ErrConsent) }}
	_, e := c.Get(context.Background(), "folder")
	if e != ErrConsent || strings.Contains(e.Error(), "SECRET") {
		t.Fatalf("wrapped token error leaked %v", e)
	}
}

func TestSDKMetadataSizeNormalization(t *testing.T) {
	for _, tc := range []struct{ name, field, want string }{
		{"missing", "", ""}, {"zero", `,"size":"0"`, ""}, {"positive", `,"size":"42"`, "42"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.Contains(r.URL.Query().Get("fields"), "size") {
					t.Error("size field not requested")
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"id":"fixture-file","name":"fixture","mimeType":"application/octet-stream"`+tc.field+`}`)
			}))
			defer provider.Close()
			c := Client{API: provider.URL, Token: func(context.Context) (string, error) { return "fixture", nil }}
			f, err := c.Get(context.Background(), "fixture-file")
			if err != nil || f.Size != tc.want {
				t.Fatalf("size %q want %q error %v", f.Size, tc.want, err)
			}
			raw, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && bytes.Contains(raw, []byte(`"size"`)) {
				t.Fatalf("unknown size exposed as known: %s", raw)
			}
		})
	}
}
