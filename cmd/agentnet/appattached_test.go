package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/protocol"
	"github.com/misunders2d/agentnet/internal/secfile"
	"github.com/misunders2d/agentnet/internal/ui"
)

type attachedBlockedOverview struct {
	ui.Provider
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *attachedBlockedOverview) Overview() (ui.Overview, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
	}
	<-p.release
	return ui.Overview{Version: protocol.Version}, nil
}

func TestAttachedOverviewVersionWhileChatIsBlocked(t *testing.T) {
	home := t.TempDir()
	p := &attachedBlockedOverview{entered: make(chan struct{}), release: make(chan struct{})}
	var page *ui.Server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page.Handler().ServeHTTP(w, r)
	}))
	defer server.Close()
	defer close(p.release)
	page = ui.New(p, strings.TrimPrefix(server.URL, "http://"), "private")
	endpoint := server.URL + "/?t=private"
	if err := secfile.Write(filepath.Join(home, uiURLFile), []byte(endpoint)); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, server.URL+"/api/overview", nil)
		if err != nil {
			finished <- err
			return
		}
		req.AddCookie(&http.Cookie{Name: "agentnet_ui", Value: "private"})
		resp, err := server.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
		finished <- err
	}()
	select {
	case <-p.entered:
	case err := <-finished:
		t.Fatalf("chat overview did not block: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("chat overview did not start")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	version, gotEndpoint, err := attachedOverview(ctx, home)
	if err != nil || version != protocol.Version || gotEndpoint != endpoint {
		t.Fatalf("version check depends on blocked chat: version=%q endpoint_matches=%v err=%v", version, gotEndpoint == endpoint, err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("version check requested another chat projection")
	}
}

func TestAttachedOverviewKeepsLocalTokenAndReleaseGuards(t *testing.T) {
	home := t.TempDir()
	writeURL := func(endpoint string) {
		t.Helper()
		if err := secfile.Write(filepath.Join(home, uiURLFile), []byte(endpoint)); err != nil {
			t.Fatal(err)
		}
	}
	for _, endpoint := range []string{"http://example.com/?t=private", "https://127.0.0.1:55/?t=private", "http://user@127.0.0.1:55/?t=private", "http://127.0.0.1:55/"} {
		writeURL(endpoint)
		if _, _, err := attachedOverview(context.Background(), home); err == nil {
			t.Fatalf("accepted unsafe endpoint %q", endpoint)
		}
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		cookie, err := r.Cookie("agentnet_ui")
		if err != nil || cookie.Value != "private" || r.URL.Path != "/api/get-app" || r.URL.RawQuery != "" {
			t.Error("readiness lost local authentication")
		}
		json.NewEncoder(w).Encode(map[string]string{"version": "dev"})
	}))
	defer server.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL, http.StatusFound)
	}))
	defer redirect.Close()
	writeURL(redirect.URL + "/?t=private")
	if _, _, err := attachedOverview(context.Background(), home); err == nil || hits.Load() != 0 {
		t.Fatal("readiness followed a cookie redirect")
	}
	// Proxy environment must not receive a local session token.
	t.Setenv("HTTP_PROXY", redirect.URL)
	t.Setenv("NO_PROXY", "")
	endpoint := server.URL + "/?t=private"
	writeURL(endpoint)
	if version, page, err := attachedOverview(context.Background(), home); err != nil || version != "dev" || page != endpoint {
		t.Fatalf("development attachment: %q %q %v", version, page, err)
	}
	if _, _, err := attachedVersion(context.Background(), home); err == nil || !strings.Contains(err.Error(), "supported release") {
		t.Fatalf("development daemon qualified for release update: %v", err)
	}
}

func TestAppAttachedReadinessFailureOffersRetry(t *testing.T) {
	var out bytes.Buffer
	r := &appRunner{home: t.TempDir(), out: &out, logf: t.Logf}
	// A shorter parent deadline exercises the same bounded failure branch
	// without waiting out the production appStartTimeout in a focused test.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	r.showAttached(ctx)
	var event appEvent
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &event); err != nil || event.Event != "error" || !strings.Contains(event.Text, "Retry") || event.URL != "" {
		t.Fatalf("unverified startup result: %+v, %v", event, err)
	}
}
