package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/notify"
)

type soundFixture struct {
	*Fixture
	requests chan bool
}

func (f *soundFixture) NotificationSound() (client.NotificationSound, error) {
	return client.NotificationSound{Source: "system", Volume: 100}, nil
}

func (f *soundFixture) ChangeNotificationSound(change client.NotificationSoundChange, preview bool) (client.NotificationSound, error) {
	if change.Volume == nil || *change.Volume != 50 || len(change.File) != notify.MaxSoundFile || change.FileName != "test.wav" {
		return client.NotificationSound{}, Refuse("Unexpected setting")
	}
	f.requests <- preview
	return client.NotificationSound{Source: change.Source, Volume: *change.Volume, FileName: change.FileName}, nil
}

func TestWindowsNotificationSoundHostRoutes(t *testing.T) {
	var server http.Handler
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.ServeHTTP(w, r) }))
	t.Cleanup(ts.Close)
	host := strings.TrimPrefix(ts.URL, "http://")
	base := NewFixture(time.Now)
	server = New(base, host, testToken).Handler()
	if got := do(t, ts, "GET", "/api/notify/sound", "", authed(ts, nil)); got.StatusCode != http.StatusNotFound {
		t.Fatalf("unsupported host returned %d", got.StatusCode)
	}

	fixture := &soundFixture{base, make(chan bool, 2)}
	server = New(fixture, host, testToken).Handler()
	volume := 50
	payload, err := json.Marshal(client.NotificationSoundChange{Source: "custom", Volume: &volume, FileName: "test.wav", File: bytes.Repeat([]byte{42}, notify.MaxSoundFile)})
	if err != nil {
		t.Fatal(err)
	}
	if got := do(t, ts, "POST", "/api/notify/sound", string(payload), authed(ts, map[string]string{"Origin": "http://other.site"})); got.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin save returned %d", got.StatusCode)
	}
	if got := do(t, ts, "POST", "/api/notify/sound-preview", "{}", nil); got.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated preview returned %d", got.StatusCode)
	}
	if got := do(t, ts, "POST", "/api/notify/sound", `{"source":"custom","path":"C:/elsewhere.wav"}`, post(ts)); got.StatusCode != http.StatusBadRequest {
		t.Fatalf("accepted path instead of selected file bytes: %d", got.StatusCode)
	}
	if got := do(t, ts, "POST", "/api/notify/sound", string(payload)+strings.Repeat(" ", maxBody+10), post(ts)); got.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized upload returned %d", got.StatusCode)
	}
	for _, path := range []string{"/api/notify/sound-preview", "/api/notify/sound"} {
		got := do(t, ts, "POST", path, string(payload), post(ts))
		var fields map[string]json.RawMessage
		if got.StatusCode != http.StatusOK || json.NewDecoder(got.Body).Decode(&fields) != nil || fields["file"] != nil || fields["file_name"] == nil {
			t.Fatalf("%s returned %d or an unexpected response: %v", path, got.StatusCode, fields)
		}
		select {
		case preview := <-fixture.requests:
			if preview != strings.HasSuffix(path, "-preview") {
				t.Fatalf("route confused preview and save: %s", path)
			}
		default:
			t.Fatalf("missing settings call: %s", path)
		}
	}
}
