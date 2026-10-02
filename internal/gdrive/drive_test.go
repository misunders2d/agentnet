package gdrive

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDriveProviderJourney(t *testing.T) {
	var mu sync.Mutex
	created, uploaded, shared := false, false, false
	token := "alice"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth := r.Header.Get("Authorization")
		if auth == "Bearer expired" {
			w.WriteHeader(401)
			io.WriteString(w, "SECRET provider body")
			return
		}
		if auth == "Bearer bob-denied" {
			w.WriteHeader(403)
			io.WriteString(w, "SECRET provider body")
			return
		}
		if auth != "Bearer alice" && auth != "Bearer bob" {
			t.Errorf("missing local bearer")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/about":
			fmt.Fprint(w, `{"user":{"permissionId":"alice-account","emailAddress":"alice@example.test"}}`)
		case r.URL.Path == "/files" && r.Method == "POST" && r.URL.Query().Get("uploadType") == "multipart":
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "project.txt") || !strings.Contains(string(b), "fixture content") {
				t.Error("upload metadata/content missing")
			}
			uploaded = true
			fmt.Fprint(w, `{"id":"file1","name":"project.txt","parents":["folder1"]}`)
		case r.URL.Path == "/files" && r.Method == "POST":
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			if m["mimeType"] != FolderMIME {
				t.Error("not folder create")
			}
			created = true
			fmt.Fprint(w, `{"id":"folder1","name":"Project","mimeType":"application/vnd.google-apps.folder","capabilities":{"canAddChildren":true,"canListChildren":true,"canShare":true}}`)
		case r.URL.Path == "/files/folder1":
			fmt.Fprint(w, `{"id":"folder1","name":"Project","mimeType":"application/vnd.google-apps.folder","capabilities":{"canAddChildren":true,"canListChildren":true,"canShare":true}}`)
		case r.URL.Path == "/files" && r.Method == "GET":
			if r.URL.Query().Get("q") != "'folder1' in parents and trashed = false" {
				t.Error("folder query escaped incorrectly")
			}
			if r.URL.Query().Get("pageToken") == "next" {
				fmt.Fprint(w, `{"files":[{"id":"file2","name":"Second"}]}`)
			} else {
				fmt.Fprint(w, `{"files":[{"id":"file1","name":"First"}],"nextPageToken":"next"}`)
			}
		case r.URL.Path == "/files/folder1/permissions" && r.Method == "POST":
			var m map[string]string
			json.NewDecoder(r.Body).Decode(&m)
			if m["emailAddress"] != "bob@example.test" || m["role"] != "writer" {
				t.Error("implicit/bad permission")
			}
			shared = true
			fmt.Fprint(w, `{"id":"perm1","type":"user","role":"writer"}`)
		case r.URL.Path == "/files/folder1/permissions" && r.Method == "GET":
			fmt.Fprint(w, `{"permissions":[{"id":"perm1","type":"user","role":"writer","permissionDetails":[{"inherited":true,"inheritedFrom":"parent"}]}],"nextPageToken":"perm-next"}`)
		case r.URL.Path == "/files/folder1/permissions/perm1" && r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected provider route %s %s", r.Method, r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c := Client{API: srv.URL, UploadAPI: srv.URL, Token: func(context.Context) (string, error) { return token, nil }}
	ctx := context.Background()
	if _, e := c.CreateFolder(ctx, "Project"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Folder(ctx, "folder1"); e != nil {
		t.Fatal(e)
	}
	p, e := c.List(ctx, "folder1", "")
	if e != nil || p.Next != "next" || len(p.Files) != 1 {
		t.Fatalf("list %v %v", p, e)
	}
	p, e = c.List(ctx, "folder1", p.Next)
	if e != nil || p.Files[0].ID != "file2" {
		t.Fatalf("page %v %v", p, e)
	}
	if _, e = c.Upload(ctx, "folder1", "project.txt", "", strings.NewReader("fixture content"), 15); e != nil {
		t.Fatal(e)
	}
	if _, e = c.Share(ctx, "folder1", "bob@example.test", "writer"); e != nil {
		t.Fatal(e)
	}
	ps, next, e := c.Permissions(ctx, "folder1", "")
	if e != nil || next != "perm-next" || !ps[0].Details[0].Inherited {
		t.Fatal("permission details missing")
	}
	if e = c.RemovePermission(ctx, "folder1", "perm1"); e != nil {
		t.Fatal(e)
	}
	token = "bob"
	if _, e = c.List(ctx, "folder1", ""); e != nil {
		t.Fatal(e)
	}
	token = "bob-denied"
	if _, e = c.List(ctx, "folder1", ""); !errors.Is(e, ErrDenied) || strings.Contains(e.Error(), "SECRET") {
		t.Fatal(e)
	}
	token = "expired"
	if _, e = c.Folder(ctx, "folder1"); !errors.Is(e, ErrConsent) {
		t.Fatal(e)
	}
	mu.Lock()
	if !created || !uploaded || !shared {
		t.Error("mutations did not reach provider")
	}
	mu.Unlock()
	srv.Close()
	token = "alice"
	if _, e = c.List(ctx, "folder1", ""); !errors.Is(e, ErrOffline) {
		t.Fatal(e)
	}
}
func TestDrivePKCELoopbackExpiryAndRevoke(t *testing.T) {
	var challenge string
	var tokenCalls, revokeCalls int
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.URL.Path == "/revoke" {
			revokeCalls++
			if r.Form.Get("token") != "refresh-fixture" {
				t.Error("wrong revoke token")
			}
			w.WriteHeader(200)
			return
		}
		tokenCalls++
		if r.Form.Get("grant_type") == "authorization_code" {
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				t.Error("PKCE mismatch")
			}
			if r.Form.Get("code") != "fixture-code" {
				t.Error("wrong code")
			}
			if !strings.HasPrefix(r.Form.Get("redirect_uri"), "http://127.0.0.1:") {
				t.Error("non-loopback redirect")
			}
		} else if r.Form.Get("refresh_token") != "refresh-fixture" {
			t.Error("wrong refresh token")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"access-fixture","refresh_token":"refresh-fixture","scope":%q,"expires_in":3600,"token_type":"Bearer"}`, FileScope)
	}))
	defer provider.Close()
	o := OAuthConfig{ClientID: "fixture-desktop-client", TokenURL: provider.URL, RevokeURL: provider.URL + "/revoke"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := make(chan Token, 1)
	link, e := o.Consent(ctx, false, func(t Token) error { saved <- t; return nil })
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(link)
	q := u.Query()
	challenge = q.Get("code_challenge")
	if q.Get("code_challenge_method") != "S256" || q.Get("scope") != FileScope {
		t.Fatal("wrong scope/PKCE")
	}
	callback := q.Get("redirect_uri")
	res, e := http.Get(callback + "?state=wrong&code=fixture-code")
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Fatal("bad state accepted")
	}
	res, e = http.Get(callback + "?state=" + url.QueryEscape(q.Get("state")) + "&code=fixture-code")
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("consent failed")
	}
	var tok Token
	select {
	case tok = <-saved:
	case <-time.After(time.Second):
		t.Fatal("no token saved")
	}
	if !tok.Has(FileScope) || time.Until(tok.Expiry) < time.Hour-time.Minute {
		t.Fatal("bad expiry")
	}
	if _, e = o.Refresh(ctx, tok); e != nil {
		t.Fatal(e)
	}
	if e = o.Revoke(ctx, tok); e != nil {
		t.Fatal(e)
	}
	if tokenCalls != 2 || revokeCalls != 1 {
		t.Fatalf("calls %d %d", tokenCalls, revokeCalls)
	}
}
func TestDriveCapabilitiesAndRedirectFailClosed(t *testing.T) {
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("bearer redirected to another origin") }))
	defer destination.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/files/noaccess" {
			fmt.Fprint(w, `{"id":"noaccess","name":"Read only","mimeType":"application/vnd.google-apps.folder","capabilities":{}}`)
			return
		}
		http.Redirect(w, r, destination.URL, 302)
	}))
	defer srv.Close()
	c := Client{API: srv.URL, Token: func(context.Context) (string, error) { return "secret", nil }}
	if _, e := c.Upload(context.Background(), "noaccess", "x", "", strings.NewReader("x"), 1); !errors.Is(e, ErrCapability) {
		t.Fatal(e)
	}
	if _, e := c.Folder(context.Background(), "redirect"); e == nil {
		t.Error("redirect accepted")
	}
	if _, e := c.List(context.Background(), "bad'query", ""); e == nil {
		t.Error("unsafe id accepted")
	}
}
