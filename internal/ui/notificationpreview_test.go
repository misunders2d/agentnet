package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/misunders2d/agentnet/internal/client"
	"github.com/misunders2d/agentnet/internal/protocol"
)

func TestNotificationChangesRendered(t *testing.T) {
	if os.Getenv("AGENTNET_PLAYWRIGHT") == "" {
		t.Skip("set AGENTNET_PLAYWRIGHT for rendered notification preference recovery")
	}
	t.Log(browserCheck(t, "testdata/notification_changes_rendered.cjs"))
}

// The actual HTTP control changes privacy alone, including while alerts are off.
func TestNotificationPreviewControlPreservesConsent(t *testing.T) {
	alice, bob, live := liveWorld(t)
	var server *Server
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { server.Handler().ServeHTTP(w, r) }))
	defer ts.Close()
	server = New(live, strings.TrimPrefix(ts.URL, "http://"), testToken)
	for _, enabled := range []bool{false, true} {
		before := client.AlertPrefs{Enabled: enabled, Mutes: []string{strings.Repeat("a", 64)}, Senders: []protocol.NotifySender{{Address: alice.Address, Fingerprint: alice.Self().Fingerprint()}}}
		if err := bob.SetAlertPrefs(before); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []string{"full", "sender", "hidden", ""} {
			response := do(t, ts, "POST", "/api/notify/preview", `{"preview":"`+mode+`"}`, post(ts))
			if response.StatusCode != http.StatusOK {
				t.Fatalf("mode%q status%d", mode, response.StatusCode)
			}
			got, err := bob.AlertPrefs()
			if err != nil {
				t.Fatal(err)
			}
			want := before
			want.Preview = client.NotificationPreview(mode)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("privacy changed consent or scope: got%+v want%+v", got, want)
			}
			view, err := live.notifyView()
			if err != nil || view.Preview != mode || view.PreviewDefault != "hidden" {
				t.Fatalf("actual host default %+v %v", view, err)
			}
		}
		// Existing API convention: refused values are 409, malformed JSON is 400.
		for _, invalid := range []struct {
			body   string
			status int
		}{
			{`{}`, http.StatusConflict}, {`{"preview":null}`, http.StatusConflict},
			{`{"preview":"FULL"}`, http.StatusConflict}, {`{"preview":1}`, http.StatusBadRequest},
			{`{"preview":"sender","extra":true}`, http.StatusBadRequest}, {`{"preview":"hidden"}{}`, http.StatusBadRequest},
		} {
			response := do(t, ts, "POST", "/api/notify/preview", invalid.body, post(ts))
			if response.StatusCode != invalid.status {
				t.Fatalf("invalid %s status%d want%d", invalid.body, response.StatusCode, invalid.status)
			}
			got, err := bob.AlertPrefs()
			if err != nil || !reflect.DeepEqual(got, before) {
				t.Fatalf("invalid request changed preferences %+v %v", got, err)
			}
		}
	}
}

// An existing Alerts implementation need not add preview support.
type legacyPreviewFixture struct{ *Fixture }

func (*legacyPreviewFixture) NotifyEnable() (string, error)            { return "", nil }
func (*legacyPreviewFixture) NotifyDisable() (string, error)           { return "", nil }
func (*legacyPreviewFixture) NotifyMute(string, bool) (string, error)  { return "", nil }
func (*legacyPreviewFixture) NotifyAllow(string, bool) (string, error) { return "", nil }
func (*legacyPreviewFixture) NotifySeen(string, []string) error        { return nil }
func TestNotificationPreviewOptionalProvider(t *testing.T) {
	f := &legacyPreviewFixture{NewFixture(time.Now)}
	server := New(f, "localhost", testToken)
	req := httptest.NewRequest("POST", "http://localhost/api/notify/preview", strings.NewReader(`{"preview":"full"}`))
	req.SetPathValue("what", "preview")
	recorder := httptest.NewRecorder()
	server.notify(recorder, req)
	if recorder.Code != http.StatusNotFound {
		t.Fatal(recorder.Code, recorder.Body.String())
	}
}

func TestNotificationPreviewSettingsPresentation(t *testing.T) {
	if os.Getenv("AGENTNET_WEB_MODULES") == "" {
		t.Skip("opt-in: existing pinned frontend modules required")
	}
	out, err := exec.Command("node", "testdata/notification_preview_check.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Log(string(out))
}
