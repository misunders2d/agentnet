package client

import (
	"bytes"
	"context"
	"fmt"
	"github.com/misunders2d/agentnet/internal/gdrive"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDriveSDKRefreshPersistenceConcurrency(t *testing.T) {
	w, conv, _ := dmWithHistory(t)
	p, _, err := w.alice.Person()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.alice.AdmitDriveSpace(p.Person, gdrive.Space{Conv: conv, Folder: "fixture-folder", Name: "fixture", Owner: p.Person, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	mu := w.alice.driveLock()
	mu.Lock()
	state, err := w.alice.readDrive()
	if err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	state.ClientID = "fixture"
	state.Token = gdrive.Token{Access: "OLD-PRIVATE", Refresh: "OLD-REFRESH", Scope: gdrive.FullScope, Expiry: time.Now().Add(-time.Hour)}
	err = w.alice.writeDrive(state)
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/token" {
			refreshes.Add(1)
			r.ParseForm()
			if r.Form.Get("refresh_token") != "OLD-REFRESH" {
				t.Error("wrong refresh")
			}
			fmt.Fprint(rw, `{"access_token":"NEW-PRIVATE","refresh_token":"ROTATED-PRIVATE","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer NEW-PRIVATE" {
			t.Error("stale access token")
		}
		if r.URL.Path == "/files/fixture-folder" {
			fmt.Fprint(rw, `{"id":"fixture-folder","name":"fixture","mimeType":"application/vnd.google-apps.folder","capabilities":{"canListChildren":true}}`)
			return
		}
		if r.URL.Path == "/files" {
			fmt.Fprint(rw, `{"files":[]}`)
			return
		}
		t.Errorf("unexpected provider path %s", r.URL.Path)
		rw.WriteHeader(404)
	}))
	defer server.Close()
	oauth := &gdrive.OAuthConfig{ClientID: "fixture", TokenURL: server.URL + "/token"}
	provider := &gdrive.Client{API: server.URL}
	const callers = 8
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := w.alice.driveCommand(context.Background(), DriveRequest{Conv: conv, Action: "list"}, oauth, provider)
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refresh count %d", refreshes.Load())
	}
	mu.Lock()
	reloaded, err := w.alice.readDrive()
	mu.Unlock()
	if err != nil || reloaded.Token.Access != "NEW-PRIVATE" || reloaded.Token.Refresh != "ROTATED-PRIVATE" || reloaded.Token.Scope != gdrive.FullScope || reloaded.Token.Expiry.Before(time.Now().Add(50*time.Minute)) {
		t.Fatalf("persisted refresh invalid %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(w.alice.home, "google-drive.age"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"NEW-PRIVATE", "ROTATED-PRIVATE", "OLD-PRIVATE", "OLD-REFRESH"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("plaintext token in encrypted file")
		}
	}
	_, err = w.alice.driveCommand(context.Background(), DriveRequest{Conv: conv, Action: "list"}, oauth, provider)
	if err != nil || refreshes.Load() != 1 {
		t.Fatalf("reloaded state re-refreshed: %v %d", err, refreshes.Load())
	}
}
