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

	"github.com/misunders2d/agentnet/internal/secfile"
)

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
		if err != nil || cookie.Value != "private" || r.URL.Path != "/api/overview" || r.URL.RawQuery != "" {
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
